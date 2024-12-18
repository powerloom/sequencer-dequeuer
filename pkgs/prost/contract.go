package prost

import (
	"context"
	"crypto/tls"
	"math/big"
	"net/http"
	"sequencer-dequeuer/config"
	protocolStateContractABIGen "sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/redis"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	log "github.com/sirupsen/logrus"
)

var Instance *protocolStateContractABIGen.Contract

var (
	Client       *ethclient.Client
	epochsInADay = 720
)

func ConfigureClient() {
	rpcClient, err := rpc.DialOptions(context.Background(), config.SettingsObj.ClientUrl, rpc.WithHTTPClient(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}))
	if err != nil {
		log.Fatal(err)
	}
	Client = ethclient.NewClient(rpcClient)
}
func ConfigureContractInstance() {
	Instance, _ = protocolStateContractABIGen.NewContract(common.HexToAddress(config.SettingsObj.ContractAddress), Client)
}

func MustQuery[K any](ctx context.Context, call func() (val K, err error)) (K, error) {
	expBackOff := backoff.NewConstantBackOff(1 * time.Second)

	var val K
	operation := func() error {
		var err error
		val, err = call()
		return err
	}
	// Use the retry package to execute the operation with backoff
	err := backoff.Retry(operation, backoff.WithMaxRetries(expBackOff, 3))
	if err != nil {
		return *new(K), err
	}
	return val, err
}

func getExpirationTime(epochID, daySize int64) time.Time {
	// DAY_SIZE in microseconds
	updatedDaySize := time.Duration(daySize) * time.Microsecond

	// Calculate the duration of each epoch
	epochDuration := updatedDaySize / time.Duration(epochsInADay)

	// Calculate the number of epochs left for the day
	remainingEpochs := epochID % int64(epochsInADay)

	// Calculate the expiration duration
	expirationDuration := epochDuration * time.Duration(remainingEpochs)

	// Set a buffer of 10 seconds to expire slightly earlier
	bufferDuration := 10 * time.Second

	// Calculate the expiration time by subtracting the buffer duration
	expirationTime := time.Now().Add(expirationDuration - bufferDuration)

	return expirationTime
}

// FetchCurrentDay fetches the current day from the contract and caches the result in Redis
func FetchCurrentDay(dataMarketAddress string) (*big.Int, error) {
	// TODO: change this to accomodate for multiple data markets and
	// verify whether they support static or dynamic data source lists

	// Check Redis cache first
	cachedDay, err := redis.Get(context.Background(), redis.DataMarketCurrentDay(dataMarketAddress))
	if err == nil {
		// Cache hit: return the cached value
		currentDay := new(big.Int)
		currentDay.SetString(cachedDay, 10)
		return currentDay, nil
	}

	// Cache miss: fetch from contract: call the `DayCounter` method
	currentDay, err := Instance.DayCounter(nil, common.HexToAddress(dataMarketAddress))
	if err != nil {
		log.Printf("Failed to fetch current day from contract: %v", err)
		return nil, err
	}

	// Fetch the current epoch from the contract
	currentEpoch, err := Instance.CurrentEpoch(nil, common.HexToAddress(dataMarketAddress))
	if err != nil {
		log.Printf("Failed to fetch current epoch from contract: %v", err)
		return nil, err
	}

	// Fetch the day size from the contract
	daySize, err := Instance.DAYSIZE(nil, common.HexToAddress(dataMarketAddress))
	if err != nil {
		log.Printf("Failed to fetch day size from contract: %v", err)
		return nil, err
	}

	// Calculate expiration time
	expirationTime := getExpirationTime(currentEpoch.EpochId.Int64(), daySize.Int64())

	// Set the current day in Redis with the calculated expiration duration
	if err := redis.Set(context.Background(), redis.DataMarketCurrentDay(dataMarketAddress), currentDay.String(), time.Until(expirationTime)); err != nil {
		log.Printf("Failed to cache current day in Redis: %v", err)
		return nil, err
	}

	return currentDay, nil
}
