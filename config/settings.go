package config

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	log "github.com/sirupsen/logrus"
)

var SettingsObj *Settings

type Settings struct {
	ClientUrl                       string
	ContractAddress                 string
	RedisHost                       string
	RedisPort                       string
	ChainID                         int64
	FullNodes                       []string
	DataMarketAddresses             []string
	DataMarketContractAddresses     []common.Address
	VerifySubmissionDataSourceIndex bool
	SlackReportingUrl               string
	RedisDB                         string
	InitialPairs                    []string
}

// Add this new method to the Settings struct
func (s *Settings) IsValidDataMarketAddress(address common.Address) bool {
	lowercaseAddress := strings.ToLower(address.Hex())
	for _, validAddress := range s.DataMarketContractAddresses {
		if strings.ToLower(validAddress.Hex()) == lowercaseAddress {
			return true
		}
	}
	return false
}

func LoadConfig() {
	fullNodes := getEnv("FULL_NODES", "[]")
	fullNodesList := []string{}
	err := json.Unmarshal([]byte(fullNodes), &fullNodesList)
	if err != nil {
		log.Fatalf("Failed to parse FULL_NODES environment variable: %v", err)
	}

	dataMarketAddresses := getEnv("DATA_MARKET_ADDRESSES", "[]")
	dataMarketAddressesList := []string{}
	err = json.Unmarshal([]byte(dataMarketAddresses), &dataMarketAddressesList)
	if err != nil {
		log.Fatalf("Failed to parse DATA_MARKET_ADDRESSES environment variable: %v", err)
	}
	if len(dataMarketAddressesList) == 0 {
		log.Fatalf("DATA_MARKET_ADDRESSES environment variable has an empty array")
	}

	chainId, err := strconv.ParseInt(getEnv("CHAIN_ID", ""), 10, 64)
	if err != nil {
		log.Fatalf("Failed to parse CHAIN_ID environment variable: %v", err)
	}

	initialPairs, err := fetchInitialPairs()
	if err != nil {
		log.Fatalf("Failed to fetch initial pairs: %v", err)
	}

	verifySubmissionDataSourceIndex, err := strconv.ParseBool(getEnv("VERIFY_SUBMISSION_DATA_SOURCE_INDEX", "false"))
	if err != nil {
		log.Fatalf("Failed to parse VERIFY_SUBMISSION_DATA_SOURCE_INDEX environment variable: %v", err)
	}

	config := Settings{
		ClientUrl:                       getEnv("PROST_RPC_URL", ""),
		ContractAddress:                 getEnv("PROTOCOL_STATE_CONTRACT", ""),
		RedisHost:                       getEnv("REDIS_HOST", ""),
		RedisPort:                       getEnv("REDIS_PORT", ""),
		SlackReportingUrl:               getEnv("SLACK_REPORTING_URL", ""),
		DataMarketAddresses:             dataMarketAddressesList,
		RedisDB:                         getEnv("REDIS_DB", ""),
		VerifySubmissionDataSourceIndex: verifySubmissionDataSourceIndex,
		FullNodes:                       fullNodesList,
		ChainID:                         chainId,
		InitialPairs:                    initialPairs,
	}

	// transforming to checksum addresses
	for _, address := range config.DataMarketAddresses {
		config.DataMarketContractAddresses = append(config.DataMarketContractAddresses, common.HexToAddress(address))
	}

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
	url := getEnv("DATA_MARKET_STATIC_SOURCE_LIST", "")
	settings, err := fetchSettingsObject(url)
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
	// Create a custom HTTP client with insecure TLS config
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// Use the custom client to make the request
	resp, err := client.Get(url)
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
