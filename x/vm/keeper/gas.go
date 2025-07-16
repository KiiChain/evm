package keeper

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/params"

	errorsmod "cosmossdk.io/errors"
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/cosmos/evm/x/vm/types"
)

// ContextPaidFeesKey is a key used to store the paid fee in the context
type ContextPaidFeesKey struct{}

// GetEthIntrinsicGas returns the intrinsic gas cost for the transaction
func (k *Keeper) GetEthIntrinsicGas(ctx sdk.Context, msg core.Message, cfg *params.ChainConfig, isContractCreation bool) (uint64, error) {
	height := big.NewInt(ctx.BlockHeight())
	homestead := cfg.IsHomestead(height)
	istanbul := cfg.IsIstanbul(height)

	return core.IntrinsicGas(msg.Data(), msg.AccessList(), isContractCreation, homestead, istanbul)
}

// RefundGas transfers the leftover gas to the sender of the message, caped to half of the total gas
// consumed in the transaction. Additionally, the function sets the total gas consumed to the value
// returned by the EVM execution, thus ignoring the previous intrinsic gas consumed during in the
// AnteHandler.
func (k *Keeper) RefundGas(ctx sdk.Context, msg core.Message, leftoverGas uint64, denom string) error {
	// Return EVM tokens for remaining gas, exchanged at the original rate.
	remaining := new(big.Int).Mul(new(big.Int).SetUint64(leftoverGas), msg.GasPrice())

	switch remaining.Sign() {
	case -1:
		// negative refund errors
		return errorsmod.Wrapf(types.ErrInvalidRefund, "refunded amount value cannot be negative %d", remaining.Int64())
	case 1:
		// Attempt to extract the paid coin from the context
		// This is used when fee abstraction is applied into the fee payment
		// If no value is found under the context, the original denom is used
		if val := ctx.Value(ContextPaidFeesKey{}); val != nil {
			// We check if a coin exists under the value and if it's not empty
			if paidCoins, ok := val.(sdk.Coins); ok && !paidCoins.IsZero() {
				// We know that only a single coin is used for EVM payments
				if len(paidCoins) != 1 {
					// This should never happen, but if it does, we return an error
					return errorsmod.Wrapf(types.ErrInvalidRefund, "expected a single coin for EVM refunds, got %d", len(paidCoins))
				}
				paidCoin := paidCoins[0]

				// Extract the coin information
				denom = paidCoin.Denom
				amount := paidCoin.Amount.BigInt()

				// Calculate the amount to refund
				// This is calculated as:
				// remaining = amount * leftoverGas / gasUsed
				remaining = new(big.Int).Div(
					new(big.Int).Mul(amount, new(big.Int).SetUint64(leftoverGas)),
					new(big.Int).SetUint64(msg.Gas()),
				)
			}
		}

		// Positive amount refund
		refundedCoins := sdk.Coins{sdk.NewCoin(denom, sdkmath.NewIntFromBigInt(remaining))}

		// Refund to sender from the fee collector module account, which is the escrow account in charge of collecting tx fees
		err := k.bankWrapper.SendCoinsFromModuleToAccount(ctx, authtypes.FeeCollectorName, msg.From().Bytes(), refundedCoins)
		if err != nil {
			err = errorsmod.Wrapf(errortypes.ErrInsufficientFunds, "fee collector account failed to refund fees: %s", err.Error())
			return errorsmod.Wrapf(err, "failed to refund %d leftover gas (%s)", leftoverGas, refundedCoins.String())
		}
	default:
		// No refund, consume gas and update the tx gas meter
	}

	return nil
}

// ResetGasMeterAndConsumeGas reset first the gas meter consumed value to zero and set it back to the new value
// 'gasUsed'
func (k *Keeper) ResetGasMeterAndConsumeGas(ctx sdk.Context, gasUsed uint64) {
	// reset the gas count
	ctx.GasMeter().RefundGas(ctx.GasMeter().GasConsumed(), "reset the gas count")
	ctx.GasMeter().ConsumeGas(gasUsed, "apply evm transaction")
}

// GasToRefund calculates the amount of gas the state machine should refund to the sender. It is
// capped by the refund quotient value.
// Note: do not pass 0 to refundQuotient
func GasToRefund(availableRefund, gasConsumed, refundQuotient uint64) uint64 {
	// Apply refund counter
	refund := gasConsumed / refundQuotient
	if refund > availableRefund {
		return availableRefund
	}
	return refund
}
