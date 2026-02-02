package suite

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cosmos/evm/tests/systemtests/clients"
	"github.com/creachadair/tomledit"
	"github.com/creachadair/tomledit/parser"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/systemtests"
)

// SystemTestSuite implements the TestSuite interface and
// provides methods for managing test lifecycle,
// sending transactions, querying state,
// and managing expected mempool state.
type SystemTestSuite struct {
	*systemtests.SystemUnderTest
	options *TestOptions

	// Clients
	EthClient    *clients.EthClient
	CosmosClient *clients.CosmosClient

	// Most recently retrieved base fee
	baseFee *big.Int

	// Expected transaction hashes
	expPendingTxs []*TxInfo
	expQueuedTxs  []*TxInfo
}

func NewSystemTestSuite(t *testing.T) *SystemTestSuite {
	ethClient, err := clients.NewEthClient()
	require.NoError(t, err)

	cosmosClient, err := clients.NewCosmosClient()
	require.NoError(t, err)

	return &SystemTestSuite{
		SystemUnderTest: systemtests.Sut,
		EthClient:       ethClient,
		CosmosClient:    cosmosClient,
	}
}

// SetupTest initializes the test suite by resetting and starting the chain, then awaiting 2 blocks
func (s *SystemTestSuite) SetupTest(t *testing.T, nodeStartArgs ...string) {
	if len(nodeStartArgs) == 0 {
		nodeStartArgs = DefaultNodeArgs()
	}

	s.ResetChain(t)
	s.StartChain(t, nodeStartArgs...)
	s.AwaitNBlocks(t, 2)
}

// GetCurrentBlockHeight returns the current block height from the specified node
func (s *SystemTestSuite) GetCurrentBlockHeight(t *testing.T, nodeID string) uint64 {
	t.Helper()
	ctx, cli, _ := s.EthClient.Setup(nodeID, "acc0")
	blockNumber, err := cli.BlockNumber(ctx)
	require.NoError(t, err, "failed to get block number from %s", nodeID)
	return blockNumber
}

// BeforeEach resets the expected mempool state and retrieves the current base fee before each test case
func (s *SystemTestSuite) BeforeEachCase(t *testing.T) {
	// Reset expected pending/queued transactions
	s.expPendingTxs = []*TxInfo{}
	s.expQueuedTxs = []*TxInfo{}

	// Get current base fee
	currentBaseFee, err := s.GetLatestBaseFee("node0")
	require.NoError(t, err)

	s.baseFee = currentBaseFee
}

// JustAfterEach checks the expected mempool state right after each test case
func (s *SystemTestSuite) AfterEachAction(t *testing.T) {
	// Check pending txs exist in mempool or already committed - concurrently
	err := s.CheckTxsPendingAsync(s.GetExpPendingTxs())
	require.NoError(t, err)

	// Check queued txs only exist in local mempool (queued txs should be only EVM txs)
	err = s.CheckTxsQueuedSync(s.GetExpQueuedTxs())
	require.NoError(t, err)

	// Wait for block commit
	s.AwaitNBlocks(t, 1)

	// Get current base fee and set it to suite.baseFee
	currentBaseFee, err := s.GetLatestBaseFee("node0")
	require.NoError(t, err)

	s.baseFee = currentBaseFee
}

// AfterEach waits for all expected pending transactions to be committed
func (s *SystemTestSuite) AfterEachCase(t *testing.T) {
	// Check all expected pending txs are committed
	for _, txInfo := range s.GetExpPendingTxs() {
		err := s.WaitForCommit(txInfo.DstNodeID, txInfo.TxHash, txInfo.TxType, time.Second*60)
		require.NoError(t, err)
	}

	// Check all evm pending txs are cleared in mempool
	for i := range s.Nodes() {
		pending, _, err := s.TxPoolContent(s.Node(i), TxTypeEVM)
		require.NoError(t, err)

		require.Len(t, pending, 0, "pending txs are not cleared in mempool")
	}

	// Check all cosmos pending txs are cleared in mempool
	for i := range s.Nodes() {
		pending, _, err := s.TxPoolContent(s.Node(i), TxTypeCosmos)
		require.NoError(t, err)

		require.Len(t, pending, 0, "pending txs are not cleared in mempool")
	}

	// Wait for block commit
	s.AwaitNBlocks(t, 1)
}

// ModifyConsensusTimeout modifies the consensus timeout_commit in the config.toml
// for all nodes and restarts the chain with the new configuration.
func (s *SystemTestSuite) ModifyConsensusTimeout(t *testing.T, timeout string, nodeStartArgs ...string) {
	t.Helper()

	// Stop the chain if running
	if s.ChainStarted {
		s.ResetChain(t)
	}

	// Modify config.toml for each node
	for i := 0; i < s.NodesCount(); i++ {
		nodeDir := s.NodeDir(i)
		configPath := filepath.Join(nodeDir, "config", "config.toml")

		err := editToml(configPath, func(doc *tomledit.Document) {
			setValue(doc, timeout, "consensus", "timeout_commit")
		})
		require.NoError(t, err, "failed to modify config.toml for node %d", i)
	}

	// Restart the chain with modified config
	s.StartChain(t, nodeStartArgs...)
	s.AwaitNBlocks(t, 2)
}

// editToml is a helper to edit TOML files
func editToml(filename string, f func(doc *tomledit.Document)) error {
	tomlFile, err := os.OpenFile(filename, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer tomlFile.Close()

	doc, err := tomledit.Parse(tomlFile)
	if err != nil {
		return fmt.Errorf("failed to parse toml: %w", err)
	}

	f(doc)

	if _, err := tomlFile.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to seek: %w", err)
	}
	if err := tomlFile.Truncate(0); err != nil {
		return fmt.Errorf("failed to truncate: %w", err)
	}
	if err := tomledit.Format(tomlFile, doc); err != nil {
		return fmt.Errorf("failed to format: %w", err)
	}

	return nil
}

// setValue sets a value in a TOML document
func setValue(doc *tomledit.Document, newVal string, xpath ...string) {
	e := doc.First(xpath...)
	if e == nil {
		panic(fmt.Sprintf("not found: %v", xpath))
	}
	e.Value = parser.MustValue(fmt.Sprintf("%q", newVal))
}
