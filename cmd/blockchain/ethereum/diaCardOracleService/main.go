package main

import (
	"context"
	"fmt"
	"github.com/diadata-org/diadata/pkg/dia/scraper/blockchain-scrapers/blockchains/ethereum/diaCardOracleService"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/diadata-org/diadata/pkg/dia"
	models "github.com/diadata-org/diadata/pkg/model"
	"github.com/diadata-org/diadata/pkg/utils"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

func main() {
	key := utils.Getenv("PRIVATE_KEY", "")
	key_password := utils.Getenv("PRIVATE_KEY_PASSWORD", "")
	deployedContract := utils.Getenv("DEPLOYED_CONTRACT", "")
	blockchainNode := utils.Getenv("BLOCKCHAIN_NODE", "")
	sleepSeconds, err := strconv.Atoi(utils.Getenv("SLEEP_SECONDS", "120"))
	if err != nil {
		log.Fatalf("Failed to parse sleepSeconds: %v")
	}
	frequencySeconds, err := strconv.Atoi(utils.Getenv("FREQUENCY_SECONDS", "120"))
	if err != nil {
		log.Fatalf("Failed to parse frequencySeconds: %v")
	}
	chainId, err := strconv.ParseInt(utils.Getenv("CHAIN_ID", "1"), 10, 64)
	if err != nil {
		log.Fatalf("Failed to parse chainId: %v")
	}

	/*
	 * Setup connection to contract, deploy if necessary
	 */

	conn, err := ethclient.Dial(blockchainNode)
	if err != nil {
		log.Fatalf("Failed to connect to the Ethereum client: %v", err)
	}

	auth, err := bind.NewTransactorWithChainID(strings.NewReader(key), key_password, big.NewInt(chainId))
	if err != nil {
		log.Fatalf("Failed to create authorized transactor: %v", err)
	}

	var contract *diaCardOracleService.DIAXDAIOracle
	err = deployOrBindContract(deployedContract, conn, auth, &contract)
	if err != nil {
		log.Fatalf("Failed to Deploy or Bind contract: %v", err)
	}
	periodicOracleUpdateHelper(sleepSeconds, auth, contract, conn)
	/*
	 * Update Oracle periodically with top coins
	 */
	ticker := time.NewTicker(time.Duration(frequencySeconds) * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				periodicOracleUpdateHelper(sleepSeconds, auth, contract, conn)
			}
		}
	}()
	select {}
}

func periodicOracleUpdateHelper(sleepSeconds int, auth *bind.TransactOpts, contract *diaCardOracleService.DIAXDAIOracle, conn *ethclient.Client) error {

	// Get quotation for CARD coin and update Oracle
	rawCardQ, err := getQuotationFromDia("CARD")
	if err != nil {
		log.Fatalf("Failed to retrieve CARD quotation data from DIA: %v", err)
		return err
	}
	rawCardQ.Name = "CARD"
	err = updateQuotation(rawCardQ, auth, contract, conn)
	if err != nil {
		log.Fatalf("Failed to update CARD Oracle: %v", err)
		return err
	}
	time.Sleep(time.Duration(sleepSeconds) * time.Second)

	rawEthQ, err := getQuotationFromDia("ETH")
	if err != nil {
		log.Fatalf("Failed to retrieve ETH quotation data from DIA: %v", err)
		return err
	}
	rawEthQ.Name = "ETH"
	err = updatePair(rawCardQ, rawEthQ, auth, contract, conn)
	if err != nil {
		log.Fatalf("Failed to update CARD/ETH Oracle: %v", err)
		return err
	}
	time.Sleep(time.Duration(sleepSeconds) * time.Second)

	return nil
}

func deployOrBindContract(deployedContract string, conn *ethclient.Client, auth *bind.TransactOpts, contract **diaCardOracleService.DIAXDAIOracle) error {
	var err error
	if deployedContract != "" {
		*contract, err = diaCardOracleService.NewDIAXDAIOracle(common.HexToAddress(deployedContract), conn)
		if err != nil {
			return err
		}
	} else {
		// deploy contract
		var addr common.Address
		var tx *types.Transaction
		addr, tx, *contract, err = diaCardOracleService.DeployDIAXDAIOracle(auth, conn)
		if err != nil {
			log.Fatalf("could not deploy contract: %v", err)
			return err
		}
		log.Printf("Contract pending deploy: 0x%x\n", addr)
		log.Printf("Transaction waiting to be mined: 0x%x\n\n", tx.Hash())
		time.Sleep(180000 * time.Millisecond)
	}
	return nil
}

func updateQuotation(quotation *models.Quotation, auth *bind.TransactOpts, contract *diaCardOracleService.DIAXDAIOracle, conn *ethclient.Client) error {
	symbol := quotation.Symbol + "/USD"
	price := quotation.Price
	timestamp := time.Now().Unix()
	err := updateOracle(conn, contract, auth, symbol, int64(price*100000000), timestamp)
	if err != nil {
		log.Fatalf("Failed to update Oracle: %v", err)
		return err
	}

	return nil
}

func updatePair(quoteQuotation *models.Quotation, baseQuotation *models.Quotation, auth *bind.TransactOpts, contract *diaCardOracleService.DIAXDAIOracle, conn *ethclient.Client) error {
	symbol := quoteQuotation.Symbol + "/" + baseQuotation.Symbol
	price := quoteQuotation.Price / baseQuotation.Price
	timestamp := time.Now().Unix()
	err := updateOracle(conn, contract, auth, symbol, int64(price*100000000), timestamp)
	if err != nil {
		log.Fatalf("Failed to update Oracle: %v", err)
		return err
	}

	return nil
}

func updateOracle(
	client *ethclient.Client,
	contract *diaCardOracleService.DIAXDAIOracle,
	auth *bind.TransactOpts,
	key string,
	value int64,
	timestamp int64) error {

	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	// Get 110% of the gas price
	fmt.Println(gasPrice)
	fGas := new(big.Float).SetInt(gasPrice)
	fGas.Mul(fGas, big.NewFloat(1.1))
	gasPrice, _ = fGas.Int(nil)
	fmt.Println(gasPrice)
	// Write values to smart contract
	tx, err := contract.SetValue(&bind.TransactOpts{
		From:     auth.From,
		Signer:   auth.Signer,
		GasLimit: 800725,
		GasPrice: gasPrice,
	}, key, big.NewInt(value), big.NewInt(timestamp))
	if err != nil {
		return err
	}
	fmt.Println(tx.GasPrice())
	log.Printf("key: %s\n", key)
	log.Printf("Tx To: %s\n", tx.To().String())
	log.Printf("Tx Hash: 0x%x\n", tx.Hash())
	return nil
}

func getQuotationFromDia(symbol string) (*models.Quotation, error) {
	response, err := http.Get(dia.BaseUrl + "v1/quotation/" + strings.ToUpper(symbol))
	if err != nil {
		return nil, err
	}

	defer response.Body.Close()
	if 200 != response.StatusCode {
		return nil, fmt.Errorf("Error on dia api with return code %d", response.StatusCode)
	}
	contents, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	var quotation models.Quotation
	err = quotation.UnmarshalBinary(contents)
	if err != nil {
		return nil, err
	}
	return &quotation, nil
}
