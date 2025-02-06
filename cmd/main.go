package main

import (
	"context"
	"sequencer-dequeuer/config"
	"sequencer-dequeuer/pkgs/dequeuer"
	"sequencer-dequeuer/pkgs/prost"
	"sequencer-dequeuer/pkgs/redis"
	"sequencer-dequeuer/pkgs/service"
	"sequencer-dequeuer/pkgs/utils"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
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

	// Run cleanup for each data market with delay
	for _, dataMarketAddr := range config.SettingsObj.DataMarketAddresses {
		wg.Add(1)
		dataMarket := dataMarketAddr // Create new variable to avoid closure issues
		go func() {
			defer wg.Done()
			if err := dequeuer.CleanupSubmissionSet(context.Background(), dataMarket); err != nil {
				log.Errorln("Failed to cleanup submission set", "error", err, "dataMarket", dataMarket)
			}
		}()
		time.Sleep(2 * time.Second) // Add delay between launches
	}

	// Start submission handler
	wg.Add(1)
	dequeuer.SubmissionHandlerInstance = &dequeuer.SubmissionHandler{}
	go func() {
		defer wg.Done()
		dequeuer.SubmissionHandlerInstance.Start()
	}()

	wg.Wait()
}
