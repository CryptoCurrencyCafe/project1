package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"github.com/btcsuite/btcec"
	"github.com/btcsuite/btcnet"
	"github.com/btcsuite/btcscript"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcwire"
)

/*

For this program to execute correctly the following needs to be provided:

- An internet connection
- A private key
- A receiving address
- The raw json of the funding transaction
- The index into that transaction that funds your private key.

The output of the program will be a valid bitcoin transaction encoded as hex
which can be submitted to any bitcoin client or website that accepts raw hex
transactions. For example: https://blockchain.info/pushtx

*/

var a = flag.String("address", "", "The address to send Bitcoin to.")
var k = flag.String("privkey", "", "The private key of the input tx.")
var t = flag.String("txid", "", "The transaction id corresponding to the funding Bitcoin transaction.")
var v = flag.Int("vout", -1, "The index into the funding transaction.")

type requiredArgs struct {
	txid      *btcwire.ShaHash
	vout      uint32
	toAddress btcutil.Address
	privKey   *btcec.PrivateKey
}

// getArgs parses command line args and asserts that a private key and an
// address are present and correctly formatted.
func getArgs() requiredArgs {
	flag.Parse()
	if *a == "" || *k == "" || *t == "" || *v == -1 {
		fmt.Println("\nThis tool generates a bitcoin transaction that moves coins from an input to an output.\n" +
			"You must provide a key, an address, a transaction id (the hash\n" +
			"of a tx) and the index into the outputs of that tx that fund your\n" +
			"address! Use http://blockchain.info/pushtx to send the raw transaction.\n")
		flag.PrintDefaults()
		fmt.Println("")
		os.Exit(0)
	}

	pkBytes, err := hex.DecodeString(*k)
	if err != nil {
		log.Fatal(err)
	}
	privKey, _ := btcec.PrivKeyFromBytes(btcec.S256(), pkBytes)

	addr, err := btcutil.DecodeAddress(*a, &btcnet.MainNetParams)
	if err != nil {
		log.Fatal(err)
	}

	txid, err := btcwire.NewShaHashFromStr(*t)

	args := requiredArgs{
		txid:      txid,
		vout:      uint32(*v),
		toAddress: addr,
		privKey:   privKey,
	}

	return args
}

type BlockChainInfoTxOut struct {
	Value     int    `json:"value"`
	ScriptHex string `json:"script"`
}

type blockChainInfoTx struct {
	Ver     int                   `json:"ver"`
	Hash    string                `json:"hash"`
	Outputs []BlockChainInfoTxOut `json:"out"`
}

// Uses the txid of the target funding transaction and asks blockchain.info's
// api for information (in json) relaated to that transaction.
func lookupTxid(hash *btcwire.ShaHash) *blockChainInfoTx {

	url := "https://blockchain.info/rawtx/" + hash.String()
	resp, err := http.Get(url)
	if err != nil {
		log.Fatal(fmt.Errorf("Tx Lookup failed: %v", err))
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(fmt.Errorf("TxInfo read failed: %s", err))
	}

	fmt.Printf("%s\n", b)
	txinfo := &blockChainInfoTx{}
	err = json.Unmarshal(b, txinfo)
	if err != nil {
		log.Fatal(err)
	}

	if txinfo.Ver != 1 {
		log.Fatal(fmt.Errorf("Blockchain.info's response seems bad: %v", txinfo))
	}

	return txinfo
}

// getFundingParams pulls the relevant transaction information from the json returned by blockchain.info
// To generate a new valid transaction all of the parameters of the TxOut we are
// spending from must be used.
func getFundingParams(rawtx *blockChainInfoTx, vout uint32) (*btcwire.TxOut, *btcwire.OutPoint) {
	fmt.Printf("%+v\n", rawtx)
	blkChnTxOut := rawtx.Outputs[vout]

	hash, err := btcwire.NewShaHashFromStr(rawtx.Hash)
	if err != nil {
		log.Fatal(err)
	}

	// Then convert it to a btcutil amount
	amnt := btcutil.Amount(int64(blkChnTxOut.Value))

	if err != nil {
		log.Fatal(err)
	}

	outpoint := btcwire.NewOutPoint(hash, vout)

	subscript, err := hex.DecodeString(blkChnTxOut.ScriptHex)
	if err != nil {
		log.Fatal(err)
	}

	oldTxOut := btcwire.NewTxOut(int64(amnt), subscript)

	return oldTxOut, outpoint
}

func main() {
	// Pull the required arguments off of the command line.
	reqArgs := getArgs()

	// Get the bitcoin tx from blockchain.info's api
	rawFundingTx := lookupTxid(reqArgs.txid)

	// Get the parameters we need from the funding transaction
	oldTxOut, outpoint := getFundingParams(rawFundingTx, reqArgs.vout)

	// Formulate a new transaction from the provided parameters
	tx := btcwire.NewMsgTx()

	// Create the TxIn
	txin := createTxIn(outpoint)
	tx.AddTxIn(txin)

	// Create the TxOut
	txout := createTxOut(oldTxOut.Value, reqArgs.toAddress)
	tx.AddTxOut(txout)

	// Generate a signature over the whole tx.
	sig := generateSig(tx, reqArgs.privKey, oldTxOut.PkScript)
	tx.TxIn[0].SignatureScript = sig

	// Dump the bytes to stdout
	dumpHex(tx)
}

// createTxIn pulls the outpoint out of the funding TxOut and uses it as a reference
// for the txin that will be placed in a new transaction.
func createTxIn(outpoint *btcwire.OutPoint) *btcwire.TxIn {
	// The second arg is the txin's signature script, which we are leaving empty
	// until the entire transaction is ready.
	txin := btcwire.NewTxIn(outpoint, []byte{})
	return txin
}

// createTxOut generates a TxOut can be added to a transaction. Instead of sending
// every coin in the txin to the target address, a fee 10,000 Satoshi is set aside.
// If this fee is left out then, nodes on the network will ignore the transaction,
// since they would otherwise be providing you a service for free.
func createTxOut(inCoin int64, addr btcutil.Address) *btcwire.TxOut {
	// Pay the minimum network fee so that nodes will broadcast the tx.
	outCoin := inCoin - 10000
	// Take the address and generate a PubKeyScript out of it
	script, err := btcscript.PayToAddrScript(addr)
	if err != nil {
		log.Fatal(err)
	}
	txout := btcwire.NewTxOut(outCoin, script)
	return txout
}

// generateSig requires a transaction, a private key, and the bytes of the raw
// scriptPubKey. It will then generate a signature over all of the outputs of
// the provided tx. This is the last step of creating a valid transaction.
func generateSig(tx *btcwire.MsgTx, privkey *btcec.PrivateKey, scriptPubKey []byte) []byte {

	// The all important signature. Each input is documented below.
	scriptSig, err := btcscript.SignatureScript(
		tx,                   // The tx to be signed.
		0,                    // The index of the txin the signature is for.
		scriptPubKey,         // The other half of the script from the PubKeyHash.
		btcscript.SigHashAll, // The signature flags that indicate what the sig covers.
		privkey,              // The key to generate the signature with.
		true,                 // The compress sig flag. This saves space on the blockchain.
	)
	if err != nil {
		log.Fatal(err)
	}

	return scriptSig
}

// dumpHex dumps the raw bytes of a Bitcoin transaction to stdout. This is the
// format that Bitcoin wire's protocol accepts, so you could connect to a node,
// send them these bytes, and if the tx was valid, the node would forward the
// tx through the network.
func dumpHex(tx *btcwire.MsgTx) {
	buf := bytes.NewBuffer(make([]byte, 0, tx.SerializeSize()))
	tx.Serialize(buf)
	hexstr := hex.EncodeToString(buf.Bytes())
	fmt.Println(hexstr)
}
