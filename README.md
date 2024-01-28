# Usage

## Run:

cd to dir project

`$ go mod tidy`

`$ go run memc_load [--logfile] [--loglevel] [--idfa] [--gaid] [--adid] [--dvid] [--pattern] [--dry] [--err_rate] [--workers] [--rename]`


## Example run

```python_profissional/homework_test_python/test 13 …
➜ go run memc_load --pattern="/home/dmitriy/Загрузки/tars/*.tsv.gz" --loglevel=info --err_rate=0.03 --rename=false --dry

```

```
INFO[0000] Starting the application                      adid="127.0.0.1:33015" dry=true dvid="127.0.0.1:33016" error_rate=0.03 gaid="127.0.0.1:33014" idfa="127.0.0.1:33013" logfile=stdout loglevel=info pattern="/home/dmitriy/Загрузки/tars/*.tsv.gz" rename=false workers=5
INFO[0000] Starting...                                  
INFO[0000] Found total 3 files in /home/dmitriy/Загрузки/tars/ 
INFO[0000] File 20170929000000.tsv.gz sheduled for processing 
INFO[0000] File 20170929000100.tsv.gz sheduled for processing 
INFO[0000] File 20170929000200.tsv.gz sheduled for processing 
INFO[0000] All 3 files are sheduled.                    
INFO[0000] Please wait for fileprocessors done the reading... 
INFO[0090] File 20170929000200.tsv.gz is read to the end and closed 
INFO[0091] File 20170929000100.tsv.gz is read to the end and closed 
INFO[0113] File 20170929000000.tsv.gz is read to the end and closed 
INFO[0113] Closing jobs chan                            
INFO[0113] Waiting for consumers to shut down           
INFO[0113] Waiting for analyzer to analyze the results  
INFO[0113] Successful load. Total processed: 10269498; Total errors: 0 
INFO[0113] Exiting                                      
INFO[0113] Execution time: 1m53.091807091s              
```
# MemcLoad_v2
