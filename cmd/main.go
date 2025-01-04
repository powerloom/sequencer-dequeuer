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
	// Initiate logger
	utils.InitLogger()

	// Load the config object
	config.LoadConfig()

	// Initialize reporting service
	service.InitializeReportingService(config.SettingsObj.SlackReportingUrl, 5*time.Second)

	// Setup redis
	redis.RedisClient = redis.NewRedisClient()

	// Set up the RPC client and contract instance
	prost.ConfigureClient()
	prost.ConfigureContractInstance()

	var wg sync.WaitGroup

	wg.Add(1)
	dequeuer.SubmissionHandlerInstance = &dequeuer.SubmissionHandler{}
	go dequeuer.SubmissionHandlerInstance.Start() // Start submission handler
	wg.Wait()
}
