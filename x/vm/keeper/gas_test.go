package keeper_test

import (
	"math/big"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/cosmos/evm/testutil/integration/os/factory"
	"github.com/cosmos/evm/testutil/integration/os/grpc"
	testkeyring "github.com/cosmos/evm/testutil/integration/os/keyring"
	erc20mocks "github.com/cosmos/evm/x/erc20/types/mocks"
	"github.com/cosmos/evm/x/vm/keeper"
	"github.com/cosmos/evm/x/vm/types"
	"go.uber.org/mock/gomock"
)

const (
	DefaultCoreMsgGasUsage = 21000
	DefaultGasPrice        = 120000
)

// TestGasRefundGas tests the refund gas exclusively without going though the state transition
// The gas part on the name refers to the file name to not generate a duplicated test name
func (suite *KeeperTestSuite) TestGasRefundGas() {
	// Create a txFactory
	grpcHandler := grpc.NewIntegrationHandler(suite.network)
	txFactory := factory.New(suite.network, grpcHandler)

	// Create a core message to use for the test
	keyring := testkeyring.New(2)
	sender := keyring.GetKey(0)
	recipient := keyring.GetAddr(1)
	coreMsg, err := txFactory.GenerateGethCoreMsg(
		sender.Priv,
		types.EvmTxArgs{
			To:       &recipient,
			Amount:   big.NewInt(100),
			GasPrice: big.NewInt(120000),
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
				sdk.NewCoin(suite.network.GetBaseDenom(), sdkmath.NewInt(DefaultCoreMsgGasUsage*DefaultGasPrice)),
			),
		},
		{
			name:        "Refund half the value as half gas was used",
			leftoverGas: DefaultCoreMsgGasUsage / 2,
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(suite.network.GetBaseDenom(), sdkmath.NewInt((DefaultCoreMsgGasUsage*DefaultGasPrice)/2)),
			),
		},
		{
			name:        "No refund as no gas was left over used",
			leftoverGas: 0,
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin(suite.network.GetBaseDenom(), sdkmath.NewInt(0)),
			),
		},
		{
			name:        "Refund with context fees, refunding the full value",
			leftoverGas: DefaultCoreMsgGasUsage,
			malleate: func(ctx sdk.Context) sdk.Context {
				// Set the fee abstraction paid fee key with a single coin
				return ctx.WithValue(
					keeper.ContextPaidFeesKey{},
					sdk.NewCoins(
						sdk.NewCoin("acoin", sdkmath.NewInt(750_000_000)),
					),
				)
			},
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin("acoin", sdkmath.NewInt(750_000_000)),
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
						sdk.NewCoin("acoin", sdkmath.NewInt(750_000_000)),
					),
				)
			},
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin("acoin", sdkmath.NewInt(750_000_000/2)),
			),
		},
		{
			name:        "Refund with context fees, no refund",
			leftoverGas: 0,
			malleate: func(ctx sdk.Context) sdk.Context {
				// Set the fee abstraction paid fee key with a single coin
				return ctx.WithValue(
					keeper.ContextPaidFeesKey{},
					sdk.NewCoins(
						sdk.NewCoin("acoin", sdkmath.NewInt(750_000_000)),
					),
				)
			},
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin("acoin", sdkmath.NewInt(0)),
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
						sdk.NewCoin("acoin", sdkmath.NewInt(750_000_000)),
						sdk.NewCoin("atwo", sdkmath.NewInt(750_000_000)),
					),
				)
			},
			expectedRefund: sdk.NewCoins(
				sdk.NewCoin("acoin", sdkmath.NewInt(0)), // We say as zero to skip the mock bank check
			),
			errContains: "expected a single coin for EVM refunds, got 2",
		},
	}

	// Iterate though the test cases
	for _, tc := range testCases {
		suite.Run(tc.name, func() {
			// Generate a cached context to not leak data between tests
			ctx, _ := suite.network.GetContext().CacheContext()

			// Create a new controller for the mock
			ctrl := gomock.NewController(suite.T())
			defer ctrl.Finish()

			// Apply the malleate function to the context
			if tc.malleate != nil {
				ctx = tc.malleate(ctx)
			}

			// Create a new mock bank keeper
			mockBankKeeper := erc20mocks.NewMockBankKeeper(ctrl)

			// Apply the expect, but only if expected refund is not zero
			if !tc.expectedRefund.IsZero() {
				mockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx sdk.Context, senderModule string, recipient sdk.AccAddress, coins sdk.Coins) error {
						if !coins.Equal(tc.expectedRefund) {
							suite.T().Errorf("expected %s, got %s", tc.expectedRefund, coins)
						}

						return nil
					})
			}

			// Initialize a new EVM keeper with the mock bank keeper
			// We need to redo this every time, since we will apply the mocked bank keeper at this step
			evmKeeper := keeper.NewKeeper(
				suite.network.App.AppCodec(),
				suite.network.App.GetKey(types.StoreKey),
				suite.network.App.GetTKey(types.StoreKey),
				authtypes.NewModuleAddress(govtypes.ModuleName),
				suite.network.App.AccountKeeper,
				mockBankKeeper,
				suite.network.App.StakingKeeper,
				suite.network.App.FeeMarketKeeper,
				suite.network.App.Erc20Keeper,
				"",
				suite.network.App.GetSubspace(types.ModuleName),
			)

			// Call the msg, not further checks are needed, all balance checks are done in the mock
			err := evmKeeper.RefundGas(
				ctx,
				coreMsg,
				tc.leftoverGas,
				suite.network.GetBaseDenom(),
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
