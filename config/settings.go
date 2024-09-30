package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sequencer-dequeuer/pkgs"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	log "github.com/sirupsen/logrus"
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
	InitialPairs              []string
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

	initialPairs, err := fetchInitialPairs()
	if err != nil {
		log.Fatalf("Failed to fetch initial pairs: %v", err)
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
		InitialPairs:      initialPairs,
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

func fetchInitialPairs() ([]string, error) {
	settings, err := fetchSettingsObject(pkgs.URL)
	if err != nil {
		return nil, err
	}

	initialPairs, err := interfaceToStringSlice(settings["initial_pairs"])
	if err != nil {
		return nil, err
	}

	return initialPairs, nil
}

func fetchSettingsObject(url string) (map[string]interface{}, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch settings: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error fetching settings: %s", resp.Status)
	}

	var settings map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		return nil, fmt.Errorf("failed to decode settings: %v", err)
	}

	return settings, nil
}

func interfaceToStringSlice(i interface{}) ([]string, error) {
	value := reflect.ValueOf(i)

	if value.Kind() != reflect.Slice {
		return nil, fmt.Errorf("not a slice: %T", i)
	}

	var pairs []string
	for idx := 0; idx < value.Len(); idx++ {
		element := value.Index(idx)
		pairs = append(pairs, element.String())
	}

	return pairs, nil
}
