package prost

import (
	"context"
	"crypto/tls"
	"fmt"
	"math/big"
	"net/http"
	"sequencer-dequeuer/config"
	protocolStateContractABIGen "sequencer-dequeuer/pkgs/contract"
	"sequencer-dequeuer/pkgs/redis"
	"sequencer-dequeuer/pkgs/snapshotterStateContract"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	log "github.com/sirupsen/logrus"
)

var (
	Client                   *ethclient.Client
	Instance                 *protocolStateContractABIGen.Contract
	SnapshotterStateInstance *snapshotterStateContract.SnapshotterStateContract
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

func ConfigureSnapshotterStateContractInstance() {
	// Extract snapshotter state contract address
	snapshotterStateAddress, err := Instance.SnapshotterState(&bind.CallOpts{})
	if err != nil {
		log.Errorf("Error fetching snapshotter state address: %s", err.Error())
	}

	// Initialize snapshotter state contract instance
	snapshotterStateInstance, err := snapshotterStateContract.NewSnapshotterStateContract(snapshotterStateAddress, Client)
	if err != nil {
		log.Fatalf("Failed to create snapshotter state contract instance: %v", err)
	}
	SnapshotterStateInstance = snapshotterStateInstance
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

// FetchCurrentDay fetches the current day from the contract and caches the result in Redis
func FetchCurrentDay(dataMarketAddress string) (*big.Int, error) {
	// Check Redis cache
	cachedDay, err := redis.Get(context.Background(), redis.DataMarketCurrentDay(dataMarketAddress))
	if err != nil {
		return nil, err
	}

	// Cache hit: return the cached value
	currentDay := new(big.Int)
	currentDay.SetString(cachedDay, 10)

	// Return error if current day is 0
	if currentDay.Cmp(big.NewInt(0)) == 0 {
		return nil, fmt.Errorf("current day is 0")
	}

	return currentDay, nil
}
