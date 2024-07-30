package main

import (
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs/dequeuer"
	"sequencer-dequeuer/pkgs/prost"
	"sequencer-dequeuer/pkgs/redis"
	"sequencer-dequeuer/pkgs/service"
	"sequencer-dequeuer/pkgs/utils"
	"sync"
	"time"
)

func main() {
	utils.InitLogger()
	config.LoadConfig()

	service.InitializeReportingService(config.SettingsObj.SlackReportingUrl, 5*time.Second)

	var wg sync.WaitGroup

	prost.ConfigureClient()
	prost.ConfigureContractInstance()
	redis.RedisClient = redis.NewRedisClient()

	wg.Add(1)
	go dequeuer.StartSubmissionHandler()
	wg.Wait()
}
