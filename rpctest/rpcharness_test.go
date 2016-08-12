// Copyright (c) 2016 The decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.
package rpctest

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	//"github.com/davecgh/go-spew/spew"
	"github.com/decred/dcrd/chaincfg"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrutil"
)

type rpcTestCase func(r *Harness, t *testing.T)

var rpcTestCases = []rpcTestCase{
	testSendFrom,
	testSendToAddress,
	testPurchaseTickets,
}

var primaryHarness *Harness

// TestMain manages the test harness and runs the tests instead of go test
// running the tests directly.
func TestMain(m *testing.M) {
	var err error
	primaryHarness, err = NewHarness(&chaincfg.SimNetParams, nil, nil)
	if err != nil {
		fmt.Println("unable to create primary harness: ", err)
		os.Exit(1)
	}

	// Initialize the primary mining node with a chain of length 41,
	// providing 25 mature coinbases to allow spending from for testing
	// purposes (CoinbaseMaturity=16 for simnet).
	if err = primaryHarness.SetUp(true, 25); err != nil {
		fmt.Println("unable to setup test chain: ", err)
		err = primaryHarness.TearDown()
		os.Exit(1)
	}

	// Run the tests
	exitCode := m.Run()

	// Clean up the primary harness created above. This includes removing
	// all temporary directories, and shutting down any created processes.
	if err := primaryHarness.TearDown(); err != nil {
		fmt.Println("unable to teardown test chain: ", err)
		os.Exit(1)
	}

	os.Exit(exitCode)
}

func TestRpcServer(t *testing.T) {
	for _, testCase := range rpcTestCases {
		testCase(primaryHarness, t)
	}
}

func testSendFrom(r *Harness, t *testing.T) {

	accountName := "sendFromTest"
	err := r.WalletRPC.CreateNewAccount(accountName)
	if err != nil {
		t.Fatal(err)
	}

	// Grab a fresh address from the wallet.
	addr, err := r.WalletRPC.GetNewAddress(accountName)
	if err != nil {
		t.Fatal(err)
	}

	amountToSend := dcrutil.Amount(1000000)
	// Check spendable balance of default account
	defaultBalanceBeforeSend, err := r.WalletRPC.GetBalanceMinConfType("default", 0, "all")
	if err != nil {
		t.Fatalf("getbalanceminconftype failed: %v", err)
	}

	// Get utxo list before send
	list, err := r.WalletRPC.ListUnspent()
	if err != nil {
		t.Fatalf("failed to get utxos")
	}
	utxosBeforeSend := make(map[string]float64)
	for _, utxo := range list {
		if utxo.Spendable {
			utxosBeforeSend[utxo.TxID] = utxo.Amount
		}
	}

	// SendFromMinConf 1000 to addr
	txid, err := r.WalletRPC.SendFromMinConf("default", addr, amountToSend, 0)
	if err != nil {
		t.Fatalf("sendfromminconf failed: %v", err)
	}

	// Check spendable balance of default account
	defaultBalanceAfterSendNoBlock, err := r.WalletRPC.GetBalanceMinConfType("default", 0, "all")
	if err != nil {
		t.Fatalf("getbalanceminconftype failed: %v", err)
	}

	// Check balance of sendfrom account
	sendFromBalanceAfterSendNoBlock, err := r.WalletRPC.GetBalanceMinConfType(accountName, 0, "all")
	if err != nil {
		t.Fatalf("getbalanceminconftype failed: %v", err)
	}
	if sendFromBalanceAfterSendNoBlock != amountToSend {
		t.Fatalf("balance for %s account incorrect:  want %v got %v",
			accountName, amountToSend, sendFromBalanceAfterSendNoBlock)
	}

	// Generate a single block, the transaction the wallet created should
	// be found in this block.
	curBlockHeight, block, _, _ := newBestBlock(r, t)

	// Check to make sure the transaction that was sent was included in the block
	if len(block.Transactions()) <= 1 {
		t.Fatalf("expected transaction not included in block")
	}
	minedTx := block.Transactions()[1]
	txSha := minedTx.Sha()
	if !bytes.Equal(txid[:], txSha.Bytes()[:]) {
		t.Fatalf("txid's don't match, %v vs. %v (actual vs. expected)",
			txSha, txid)
	}

	// Generate another block, since it takes 2 blocks to validate a tx
	_, err = r.GenerateBlock(curBlockHeight)
	if err != nil {
		t.Fatal(err)
	}

	// Get rawTx of sent txid so we can calculate the fee that was used
	rawTx, err := r.chainClient.GetRawTransaction(txid)
	if err != nil {
		t.Fatalf("getrawtransaction failed: %v", err)
	}

	var totalSpent int64
	for _, txIn := range rawTx.MsgTx().TxIn {
		totalSpent += txIn.ValueIn
	}

	var totalSent int64
	for _, txOut := range rawTx.MsgTx().TxOut {
		totalSent += txOut.Value
	}

	fee := dcrutil.Amount(totalSpent - totalSent)

	// Calculate the expected balance for the default account after the tx was sent
	expectedBalance := defaultBalanceBeforeSend - (amountToSend + fee)

	if expectedBalance != defaultBalanceAfterSendNoBlock {
		t.Fatalf("balance for %s account incorrect: want %v got %v", "default",
			expectedBalance, defaultBalanceAfterSendNoBlock)
	}

	time.Sleep(10 * time.Second)
	// Check balance of sendfrom account
	sendFromBalanceAfterSend1Block, err := r.WalletRPC.GetBalanceMinConfType(accountName, 1, "all")
	if err != nil {
		t.Fatalf("getbalanceminconftype failed: %v", err)
	}

	if sendFromBalanceAfterSend1Block != amountToSend {
		t.Fatalf("balance for %s account incorrect:  want %v got %v",
			accountName, amountToSend, sendFromBalanceAfterSend1Block)
	}

	// We have confirmed that the expected tx was mined into the block.
	// We should now check to confirm that the utxo that wallet used to create
	// that sendfrom was properly marked to spent and removed from utxo set.
	list, err = r.WalletRPC.ListUnspent()
	if err != nil {
		t.Fatalf("failed to get utxos")
	}
	for _, utxo := range list {
		if utxo.TxID == rawTx.MsgTx().TxIn[0].PreviousOutPoint.Hash.String() {
			t.Fatalf("found a utxo that should have been marked spent")
		}
	}
}

func testSendToAddress(r *Harness, t *testing.T) {
	// Wallet RPC client
	wcl := r.WalletRPC

	// Grab a fresh address from the wallet.
	addr, err := wcl.GetNewAddress("default")
	if err != nil {
		t.Fatal(err)
	}

	// Check spendable balance of default account
	_, err = wcl.GetBalanceMinConfType("default", 1, "spendable")
	if err != nil {
		t.Fatalf("GetBalanceMinConfType failed: %v", err)
	}

	// SendFromMinConf 1000 to addr
	txid, err := wcl.SendToAddress(addr, 1000000)
	if err != nil {
		t.Fatalf("SendToAddress failed: %v", err)
	}

	// Generate a single block, in which the transaction the wallet created
	// should be found.
	_, block, _, _ := newBestBlock(r, t)

	if len(block.Transactions()) <= 1 {
		t.Fatalf("expected transaction not included in block")
	}
	// Confirm that the expected tx was mined into the block.
	minedTx := block.Transactions()[1]
	txSha := minedTx.Sha()
	if !bytes.Equal(txid[:], txSha.Bytes()[:]) {
		t.Fatalf("txid's don't match, %v vs %v", txSha, txid)
	}

	// We should now check to confirm that the utxo that wallet used to create
	// that sendfrom was properly marked as spent and removed from utxo set.

	// Try this a different way, without another ListUnspent call.  Use
	// GetTxOut to tell if the outpoint is spent.

	// The spending transaction has to be off the tip block for the previous
	// outpoint to be spent, out of the UTXO set. Generate another block.
	_, err = r.GenerateBlock(block.MsgBlock().Header.Height)
	if err != nil {
		t.Fatal(err)
	}

	// Check each PreviousOutPoint for the sending tx.

	// Get the sending Tx
	Tx, err := wcl.GetRawTransaction(txid)
	if err != nil {
		t.Fatal("Unable to get raw transaction for", Tx)
	}
	// txid is rawTx.MsgTx().TxIn[0].PreviousOutPoint.Hash

	// Check all inputs
	for ii, txIn := range Tx.MsgTx().TxIn {
		prevOut := &txIn.PreviousOutPoint
		t.Logf("Checking previous outpoint %v, %v", ii, prevOut.String())

		// If a txout is spent (not in the UTXO set) GetTxOutResult will be nil
		res, _ := wcl.GetTxOut(&prevOut.Hash, prevOut.Index, false)
		if res != nil {
			t.Errorf("Transaction output %v still unspent.", ii)
		}
	}
}

func testPurchaseTickets(r *Harness, t *testing.T) {
	// Grab a fresh address from the wallet.
	addr, err := r.WalletRPC.GetNewAddress("default")
	if err != nil {
		t.Fatal(err)
	}
	// Set various variables for the test
	minConf := 0
	numTicket := 20
	expiry := 0
	desiredHeight := uint32(150)

	// Get current blockheight to make sure chain is at the desiredHeight
	curBlockHeight, _, _, _ := getBestBlock(r, t)

	// Keep generating blocks until desiredHeight is achieved
	for curBlockHeight < desiredHeight {
		_, err = r.WalletRPC.PurchaseTicket("default", 100000000,
			&minConf, addr, &numTicket, nil, nil, &expiry)
		// allow ErrSStxPriceExceedsSpendLimit (TODO)
		if err != nil && err.Error() != "-4: ticket price exceeds spend limit" {
			t.Fatal(err)
		}
		curBlockHeight, _, _, _ = newBlockAt(curBlockHeight, r, t)
	}

	// TODO: test pool fees

}

///////////////////////////////////////////////////////////////////////////////
// Helper functions

func newBlockAt(currentHeight uint32, r *Harness,
	t *testing.T) (uint32, *dcrutil.Block, []*chainhash.Hash, error) {

	blockHashes, err := r.GenerateBlock(currentHeight)
	if err != nil {
		t.Fatalf("Unable to generate single block: %v", err)
	}

	block, err := r.Node.GetBlock(blockHashes[0])
	if err != nil {
		t.Fatalf("Unable to get block: %v", err)
	}

	height := block.MsgBlock().Header.Height

	return height, block, blockHashes, err
}

func getBestBlock(r *Harness, t *testing.T) (uint32, *dcrutil.Block, *chainhash.Hash, error) {
	bestBlockHash, err := r.Node.GetBestBlockHash()
	if err != nil {
		t.Fatalf("Unable to get best block hash: %v", err)
	}
	bestBlock, err := r.Node.GetBlock(bestBlockHash)
	if err != nil {
		t.Fatalf("Unable to get block: %v", err)
	}
	curBlockHeight := bestBlock.MsgBlock().Header.Height

	return curBlockHeight, bestBlock, bestBlockHash, err
}

func newBestBlock(r *Harness,
	t *testing.T) (uint32, *dcrutil.Block, []*chainhash.Hash, error) {
	height, _, _, _ := getBestBlock(r, t)
	height, block, blockHash, err := newBlockAt(height, r, t)
	return height, block, blockHash, err
}
