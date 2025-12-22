package vm

import (
	"math/big"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/evm/testutil/integration/evm/factory"
	"github.com/cosmos/evm/testutil/integration/evm/grpc"
	"github.com/cosmos/evm/testutil/integration/evm/network"
	testkeyring "github.com/cosmos/evm/testutil/keyring"
	"github.com/cosmos/evm/x/vm/keeper"
	"github.com/cosmos/evm/x/vm/types"
	"github.com/ethereum/go-ethereum/params"
)

const (
	DefaultCoreMsgGasUsage = 21000
	DefaultGasPrice        = 120000
	TestDenom              = "acoin"
)

// TestGasRefundGas tests the refund gas exclusively without going though the state transition
// The gas part on the name refers to the file name to not generate a duplicated test name
func (suite *KeeperTestSuite) TestGasRefundGas() {
	// FeeCollector account is pre-funded with enough tokens
	// for refund to work
	// NOTE: everything should happen within the same block for
	// feecollector account to remain funded
	baseDenom := types.GetEVMCoinDenom()

	coins := sdk.NewCoins(
		sdk.NewCoin(
			baseDenom,
			sdkmath.NewInt(6e18),
		),
		sdk.NewCoin(
			TestDenom,
			sdkmath.NewInt(6e18),
		),
	)
	balances := []banktypes.Balance{
		{
			Address: authtypes.NewModuleAddress(authtypes.FeeCollectorName).String(),
			Coins:   coins,
		},
	}
	bankGenesis := banktypes.DefaultGenesisState()
	bankGenesis.Balances = balances
	customGenesis := network.CustomGenesisState{}
	customGenesis[banktypes.ModuleName] = bankGenesis

	// Create a txFactory
	keyring := testkeyring.New(2)
	unitNetwork := network.NewUnitTestNetwork(
		suite.Create,
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
		network.WithCustomGenesis(customGenesis),
	)
	grpcHandler := grpc.NewIntegrationHandler(unitNetwork)
	txFactory := factory.New(unitNetwork, grpcHandler)

	// Create a core message to use for the test
	sender := keyring.GetKey(0)
	recipient := keyring.GetAddr(1)
	coreMsg, err := txFactory.GenerateGethCoreMsg(
		sender.Priv,
		types.EvmTxArgs{
			To:       &recipient,
			Amount:   big.NewInt(100),
			GasPrice: big.NewInt(DefaultGasPrice),
		},
	)
	suite.Require().NoError(err)

	// Produce all the test cases
	testCases := []struct {
		name           string
		leftoverGas    uint64 // The coreMsg always uses 21000 gas limit
		malleate       func(sdk.Context) sdk.Context
		expectedRefund sdk.Coins
		errContains    string
	}{
		{
			name:        "Refund the full value as no gas was used",
			leftoverGas: DefaultCoreMsgGasUsage,
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(baseDenom, sdkmath.NewInt(DefaultCoreMsgGasUsage*DefaultGasPrice)),
			),
		},
		{
			name:        "Refund half the value as half gas was used",
			leftoverGas: DefaultCoreMsgGasUsage / 2,
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(baseDenom, sdkmath.NewInt((DefaultCoreMsgGasUsage*DefaultGasPrice)/2)),
			),
		},
		{
			name:        "No refund as no gas was left over used",
			leftoverGas: 0,
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(baseDenom, sdkmath.NewInt(0)),
			),
		},
		{
			name:        "Refund with context fees, refunding the almost full value",
			leftoverGas: DefaultCoreMsgGasUsage - 1,
			malleate: func(ctx sdk.Context) sdk.Context {
				// Set the fee abstraction paid fee key with a single coin
				return ctx.WithValue(
					keeper.ContextPaidFeesKey{},
					sdk.NewCoins(
						sdk.NewCoin(TestDenom, sdkmath.NewInt(750_000_000)),
					),
				)
			},
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(TestDenom, sdkmath.NewInt(750_000_000)),
			),
		},
		{
			name:        "Refund with context fees, refunding the half the value",
			leftoverGas: DefaultCoreMsgGasUsage / 2,
			malleate: func(ctx sdk.Context) sdk.Context {
				// Set the fee abstraction paid fee key with a single coin
				return ctx.WithValue(
					keeper.ContextPaidFeesKey{},
					sdk.NewCoins(
						sdk.NewCoin(TestDenom, sdkmath.NewInt(750_000_000)),
					),
				)
			},
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(TestDenom, sdkmath.NewInt(750_000_000/2)),
			),
		},
		{
			name:        "Refund with context fees, no refund",
			leftoverGas: 1,
			malleate: func(ctx sdk.Context) sdk.Context {
				// Set the fee abstraction paid fee key with a single coin
				return ctx.WithValue(
					keeper.ContextPaidFeesKey{},
					sdk.NewCoins(
						sdk.NewCoin(TestDenom, sdkmath.NewInt(750_000_000)),
					),
				)
			},
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(TestDenom, sdkmath.NewInt(0)),
			),
		},
		{
			name:        "Error - More than one coin being passed",
			leftoverGas: DefaultCoreMsgGasUsage,
			malleate: func(ctx sdk.Context) sdk.Context {
				// Set the fee abstraction paid fee key with a single coin
				return ctx.WithValue(
					keeper.ContextPaidFeesKey{},
					sdk.NewCoins(
						sdk.NewCoin(TestDenom, sdkmath.NewInt(750_000_000)),
						sdk.NewCoin("atwo", sdkmath.NewInt(750_000_000)),
					),
				)
			},
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(TestDenom, sdkmath.NewInt(0)), // We say as zero to skip the mock bank check
			),
			errContains: "expected a single coin for EVM refunds, got 2",
		},
	}

	// Iterate though the test cases
	for _, tc := range testCases {
		suite.Run(tc.name, func() {
			// Generate a cached context to not leak data between tests
			ctx, _ := unitNetwork.GetContext().CacheContext()

			// Apply the malleate function to the context
			if tc.malleate != nil {
				ctx = tc.malleate(ctx)
			}

			vmdb := unitNetwork.GetStateDB()
			vmdb.AddRefund(params.TxGas)

			if tc.leftoverGas > DefaultCoreMsgGasUsage {
				return
			}

			gasUsed := DefaultCoreMsgGasUsage - tc.leftoverGas

			err = unitNetwork.App.GetEVMKeeper().RefundGas(
				ctx,
				*coreMsg,
				tc.leftoverGas,
				gasUsed,
				baseDenom,
			)

			// Check the error
			if tc.errContains != "" {
				suite.Require().ErrorContains(err, tc.errContains, "RefundGas should return an error")
			} else {
				suite.Require().NoError(err, "RefundGas should not return an error")
			}
		})
	}

}
