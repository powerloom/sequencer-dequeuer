package config

import (
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	log "github.com/sirupsen/logrus"
	"os"
	"strconv"
)

var SettingsObj *Settings

type Settings struct {
	ClientUrl                 string
	ContractAddress           string
	RedisHost                 string
	RedisPort                 string
	ChainID                   int64
	FullNodes                 []string
	DataMarketAddress         string
	DataMarketContractAddress common.Address
	SlackReportingUrl         string
	RedisDB                   string
}

func LoadConfig() {
	fullNodes := getEnv("FULL_NODES", "[]")
	fullNodesList := []string{}
	err := json.Unmarshal([]byte(fullNodes), &fullNodesList)
	if err != nil {
		log.Fatalf("Failed to parse FULL_NODES environment variable: %v", err)
	}

	chainId, err := strconv.ParseInt(getEnv("CHAIN_ID", ""), 10, 64)
	if err != nil {
		log.Fatalf("Failed to parse CHAIN_ID environment variable: %v", err)
	}

	config := Settings{
		ClientUrl:         getEnv("PROST_RPC_URL", ""),
		ContractAddress:   getEnv("PROTOCOL_STATE_CONTRACT", ""),
		RedisHost:         getEnv("REDIS_HOST", ""),
		RedisPort:         getEnv("REDIS_PORT", ""),
		SlackReportingUrl: getEnv("SLACK_REPORTING_URL", ""),
		DataMarketAddress: getEnv("DATA_MARKET_ADDRESS", ""),
		RedisDB:           getEnv("REDIS_DB", ""),
		FullNodes:         fullNodesList,
		ChainID:           chainId,
	}

	config.DataMarketContractAddress = common.HexToAddress(config.DataMarketAddress)

	SettingsObj = &config
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
