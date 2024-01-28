package main

import (
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/golang/protobuf/proto"
	log "github.com/sirupsen/logrus"
)


// StartOptions - структура для хранения параметров запуска приложения
type StartOptions struct {
	Device_memc map[string]string // Карта с адресами мемкешей для каждого типа устройств
	Pattern     *string           // Шаблон для поиска файлов
	Dry         *bool            // Сухой запуск без записи в мемкеш
	Err_rate    *float64         // Допустимая скорость ошибок
	Workers     *int             // Количество рабочих горутин
	Rename      *bool            // Переименовывать обработанные файлы или нет
}

// AppsInstalled - структура для хранения данных об установленных приложениях на устройстве
type AppsInstalled struct {
	Dev_type string
	Dev_id   string
	Lat      float64
	Lon      float64
	Apps     []uint64
}

// Job - структура для хранения задачи на обработку
type Job struct {
	Appsinstalled AppsInstalled
	Memc          *memcache.Client
	Address       string
	Dry           bool
	Err           error
}

// consume - функция для обработки задач из канала jobs и записи ошибок в канал errs
func consume(jobs <-chan *Job, errs chan<- *Job, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range jobs {
		err := insertAppsinstalled(
			job.Appsinstalled, job.Memc, job.Address, job.Dry)
		if err == memcache.ErrServerError {
			log.Panicf("Memc server %s is not responding", job.Address)
		}
		job.Err = err
		errs <- job
	}
}

// analyze - функция для анализа результатов обработки задач и подсчета количества обработанных задач и ошибок
func analyze(jobs <-chan *Job, results chan<- map[string]int) {
	results_map := map[string]int{
		"processed":  0,
		"errors":     0,
	}
	for job := range jobs {
		if job.Err != nil {
			results_map["errors"]++
		} else {
			results_map["processed"]++
		}
	}
	results <- results_map
}

// processFile - функция для обработки файла
func processFile(
	f fs.FileInfo,
	dir string,
	jobs chan<- *Job,
	errs chan<- *Job,
	memc_pool map[string]*memcache.Client,
	opts StartOptions,
	wg *sync.WaitGroup,
) {

	defer wg.Done()
	defer log.Infof("File %s is read to the end and closed", f.Name())

	// Открытие и разархивирование файла
	file_path := fmt.Sprintf("%s/%s", dir, f.Name())
	fd, err := os.Open(file_path)
	if err != nil {
		log.Errorf("Cannot open file %s. Error: %s", f.Name(), err)
	}
	defer fd.Close()

	fz, err := gzip.NewReader(fd)
	if err != nil {
		log.Errorf("Cannot gunzip file %s. Error: %s", f.Name(), err)
	}
	defer fz.Close()

	// Итерация по каждой строке в файле
	scanner := bufio.NewScanner(fz)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		appsinstalled, err := parseAppinstalled(line)
		if err != nil {
			log.Debug(err)
			errs <- &Job{Err: err}
		} else {
			address := opts.Device_memc[appsinstalled.Dev_type]
			memc := memc_pool[appsinstalled.Dev_type]

			// Расписание задачи
			jobs <- &Job{
				Appsinstalled: appsinstalled,
				Memc:          memc,
				Address:       address,
				Dry:           *opts.Dry,
				Err:           nil,
			}
		}
	}
}

// processLog - функция для обработки логов
func processLog(opts StartOptions) {

	log.Info("Starting...")
	files, dir, err := getFiles(*opts.Pattern)
	if err != nil {
		log.Error("Failed to get files to parse")
		os.Exit(1)
	}
	if len(files) == 0 {
		log.Info("Everything is up-to-date. Nothing to parse")
		os.Exit(0)
	}

	log.Info(fmt.Sprintf("Found total %v files in %s", len(files), dir))
	memc_pool := getMemcPool(opts.Device_memc)

	// Инициализация каналов
	jobs := make(chan *Job, 100)         // Буферизованный канал
	errs := make(chan *Job, 100)         // Буферизованный канал
	results := make(chan map[string]int) // Небуферизованный канал
	var consumer_wg sync.WaitGroup
	var fileprocessor_wg sync.WaitGroup

	// Запуск горутин для обработки задач
	for i := 0; i < *opts.Workers; i++ {
		consumer_wg.Add(1)
		go consume(jobs, errs, &consumer_wg)
	}

	go analyze(errs, results)

	for _, f := range files {
		fileprocessor_wg.Add(1)
		go processFile(f, dir, jobs, errs, memc_pool, opts, &fileprocessor_wg)
		log.Info(fmt.Sprintf("File %s sheduled for processing", f.Name()))
	}

	log.Infof("All %v files are sheduled.", len(files))
	log.Infof("Please wait for fileprocessors done the reading...")
	fileprocessor_wg.Wait()

	log.Infof("Closing jobs chan")
	close(jobs)

	log.Infof("Waiting for consumers to shut down")
	consumer_wg.Wait()
	close(errs)

	log.Infof("Waiting for analyzer to analyze the results")
	processing_results := <-results
	close(results)
	log.Debug("All results are counted. Checking error rate")

	// Проверка скорости ошибок
	var err_rate float64
	errors := processing_results["errors"]
	processed := processing_results["processed"]
	if processed != 0 {
		err_rate = float64(errors) / float64(processed)
	} else {
		err_rate = 1.0
	}
	if err_rate >= *opts.Err_rate {
		log.Errorf(
			"High error rate (%.2f > %v). Failed load",
			err_rate,
			*opts.Err_rate,
		)
	} else {
		log.Infof(
			"Successful load. Total processed: %d; Total errors: %d",
			processed,
			errors,
		)
	}

	for _, f := range files {

		file_path := fmt.Sprintf("%s/%s", dir, f.Name())
		if *opts.Rename != false {
			if err := dotRenameFile(file_path); err == nil {
				log.Info(fmt.Sprintf("File %s renamed", f.Name()))
			}
		}
	}

	log.Info("Exiting")
}

// insertAppsinstalled - функция для записи данных об установленных приложениях в мемкеш
func insertAppsinstalled(
	appsinstalled AppsInstalled,
	memc *memcache.Client,
	address string, dry bool) error {

	uapps := &UserApps{}
	uapps.Lat = appsinstalled.Lat
	uapps.Lon = appsinstalled.Lon
	uapps.Apps = appsinstalled.Apps

	out, err := proto.Marshal(uapps)
	if err != nil {
		log.Error("Failed to encode user apps:", err)
	}

	key := fmt.Sprintf("%s:%s", appsinstalled.Dev_type, appsinstalled.Dev_id)

	if dry {

		// Псевдозапись в мемкеш
		var apps []string
		for _, i := range uapps.GetApps() {
			apps = append(apps, strconv.FormatUint(i, 10))
		}
		log.Debug(
			fmt.Sprintf("%s - %s -> %s", address, key, strings.Join(apps, " ")))
		return nil
	} else {

		// Реальная запись в мемкеш
		err := setReconnect(memc, address, memcache.Item{
			Key:   key,
			Value: out,
		})
		if err != nil {
			log.Errorf("Cannot write to memc %s key %s. Error: %s", address, key, err)
			return memcache.ErrServerError
		} else {
			log.Debug(
				fmt.Sprintf("Writing to memc server %s: key %s", address, key))
			return nil
		}
	}
}

// setReconnect - функция для установки данных в мемкеш с переподключением в случае ошибки
func setReconnect(memc *memcache.Client, address string, data memcache.Item) error {
	if err := memc.Ping(); err != nil {
		r := rand.New(rand.NewSource(42))
		for i := 0; i < 3; i++ {
			log.Warningf("Trying connect to %s, attempt %v", address, i+1)

			cap := 1.5        // Максимальное время ожидания между попытками
			base := 1.0       // Базовый множитель (экспоненциальная скорость)
			temp := math.Min( // Минимум между cap и (base*2**i)
				cap,
				math.Pow(base*2, float64(i)),
			)
			random := r.Float64() * (temp / 2) // Случайное число между 0 и temp / 2

			time.Sleep(
				// Джиттер
				time.Duration(temp/2+random) * time.Second)

			if err := memc.Ping(); err == nil {
				ok := memc.Set(&data)
				return ok
			}
		}
	}
	return fmt.Errorf("Failed to connect to %s", address)
}

// parseAppinstalled - функция для разбора строки с данными об установленных приложениях
func parseAppinstalled(line string) (AppsInstalled, error) {
	line = strings.TrimSpace(line)
	prts := strings.Split(line, "\t")
	if len(prts) != 5 {
		log.Infof("Cannot parse line: %s", line)
		return AppsInstalled{}, fmt.Errorf("Cannot parse line: %s", line)
	}
	dev_type, dev_id := prts[0], prts[1]
	lat, errlat := strconv.ParseFloat(prts[2], 2)
	lon, errlon := strconv.ParseFloat(prts[3], 2)
	if errlat != nil || errlon != nil {
		log.Infof("Cannot parse geocoords: %s", line)
		return AppsInstalled{}, fmt.Errorf("Cannot parse geocoords: %s", line)
	}
	raw_apps := strings.Split(prts[4], ",")
	var apps []uint64
	for _, raw_app := range raw_apps {
		raw_app := strings.TrimSpace(raw_app)
		app, err := strconv.ParseUint(raw_app, 0, 64)
		if err == nil {
			apps = append(apps, app)
		}
	}
	if len(apps) == 0 {
		log.Infof("Cannot parse apps: %s", line)
		return AppsInstalled{}, fmt.Errorf("Cannot parse apps: %s", line)
	}
	return AppsInstalled{dev_type, dev_id, lat, lon, apps}, nil
}

// getMemcPool - функция для получения пула клиентов мемкешей для каждого типа устройств
func getMemcPool(device_memc map[string]string) map[string]*memcache.Client {
	pool := make(map[string]*memcache.Client)
	for device_name, addr := range device_memc {
		cl := memcache.New(addr)
		pool[device_name] = cl
	}
	return pool
}

// Iterate over <dir> in given pattern and return all files
// matching <pattern>:
// 	Usage:
// 		files, err = getFiles("/misc/tarz/.*.tar.gz")
// getFiles - функция для получения списка файлов, соответствующих указанному шаблону 
func getFiles(pattern string) ([]fs.FileInfo, string, error) { dir, file_pattern := filepath.Split(pattern) 
	fsys := os.DirFS(dir) 
	if file_pattern == "" { log.Warning( "File pattern is a bare directory without actual pattern: ", pattern) 
	log.Warning("All files in dir would be processed") }

files, err := filepath.Glob(pattern)
if err != nil {
	log.Errorf("Bad pattern %s", pattern)
}

var matched_files []fs.FileInfo

for _, f := range files {
	filebase := filepath.Base(f)
	if !strings.HasPrefix(filebase, ".") {
		finfo, err := fs.Stat(fsys, filebase)
		if err != nil {
			log.Errorf("Cannot get fileinfo from %s. Error: %s", filebase, err)
			os.Exit(1)
		}
		matched_files = append(matched_files, finfo)
	}
}

return matched_files, dir, nil
}

// setLogging - функция для настройки логирования 
func setLogging(settings map[string]string) {

	// Настройка вывода логов
	logfile := settings["logfile"]
	var file *os.File
	var err error
	if logfile != "stdout" {
		file, err = os.OpenFile(logfile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		file = os.Stdout
	}
	log.SetOutput(file)

	// Настройка уровня логирования
	loglevel := settings["loglevel"]
	switch loglevel {
	case "info":
		log.SetLevel(log.InfoLevel)
	case "debug":
		log.SetLevel(log.DebugLevel)
	}
}

// getDefaultPattern - функция для получения значения по умолчанию для шаблона поиска файлов 
func getDefaultPattern() string {
	default_dir := os.Getenv("default_dir")
	if default_dir == "" {
		default_dir = "."
	}
	default_pattern := fmt.Sprintf("%s/*.tsv.gz", default_dir)
	return default_pattern
}

// dotRenameFile - функция для переименования файла с добавлением точки в начало имени
func dotRenameFile(old_path string) error {
	s := strings.Split(old_path, "/")
	filename := s[len(s)-1]
	dotted_filename := fmt.Sprintf(".%s", filename)
	s[len(s)-1] = dotted_filename
	new_path := strings.Join(s, "/")
	if err := os.Rename(old_path, new_path); err != nil {
		log.Error(fmt.Sprintf("Cannot rename file %s: Error: %s", old_path, err))
		return err
	}
	return nil
}

func main() {

	logfile := flag.String("logfile", "stdout", "Logfile name")
	loglevel := flag.String("loglevel", "info", "For debug level use debug")
	idfa := flag.String("idfa", "127.0.0.1:33013", "idfa address")
	gaid := flag.String("gaid", "127.0.0.1:33014", "gaid address")
	adid := flag.String("adid", "127.0.0.1:33015", "adid address")
	dvid := flag.String("dvid", "127.0.0.1:33016", "dvid address")
	pattern := flag.String("pattern", getDefaultPattern(), "example: <dir>/*.tsv.gz")
	dry := flag.Bool("dry", false, "turn in dryrun (without actual memcaching)")
	err_rate := flag.Float64(
		"err_rate", 0.01, "Use float64 for defining acceptable error rate")
	workers := flag.Int("workers", 5, "Number of workers (5 by default)")
	rename := flag.Bool("rename", true, "false to disable renaming processed files")
	flag.Parse()

	// Настройка логирования 
	logset := make(map[string]string)
	logset["logfile"] = *logfile
	logset["loglevel"] = *loglevel
	setLogging(logset)

	// pack all device_memc addresses into a map
	device_memc := make(map[string]string)
	device_memc["idfa"] = *idfa
	device_memc["gaid"] = *gaid
	device_memc["adid"] = *adid
	device_memc["dvid"] = *dvid

	log.WithFields(log.Fields{
		"loglevel":   *loglevel,
		"logfile":    *logfile,
		"idfa":       *idfa,
		"gaid":       *gaid,
		"adid":       *adid,
		"dvid":       *dvid,
		"dry":        *dry,
		"pattern":    *pattern,
		"error_rate": *err_rate,
		"workers":    *workers,
		"rename":     *rename,
	}).Info("Starting the application")

	start := time.Now()
	processLog(StartOptions{device_memc, pattern, dry, err_rate, workers, rename})
	log.Info(fmt.Sprintf("Execution time: %s", time.Since(start)))
}
