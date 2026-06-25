package keeper

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	"github.com/cosmos/evm/server/config"
	"github.com/cosmos/evm/x/vm/statedb"
	"github.com/cosmos/evm/x/vm/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// CallEVM performs a smart contract method call using given args.
// Note: if you call this from a precompile context, ensure that
// you use the existing stateDB.
func (k Keeper) CallEVM(ctx sdk.Context, stateDB *statedb.StateDB, abi abi.ABI, from, contract common.Address, commit, callFromPrecompile bool, gasCap *big.Int, method string, args ...interface{}) (*types.MsgEthereumTxResponse, error) {
	data, err := abi.Pack(method, args...)
	if err != nil {
		return nil, errorsmod.Wrap(
			types.ErrABIPack,
			errorsmod.Wrap(err, "failed to create transaction data").Error(),
		)
	}

	resp, err := k.CallEVMWithData(ctx, stateDB, from, &contract, data, commit, callFromPrecompile, gasCap)
	if err != nil {
		return resp, errorsmod.Wrapf(err, "contract call failed: method '%s', contract '%s'", method, contract)
	}
	return resp, nil
}

// CallEVMWithData performs a smart contract method call using contract data.
// Note: if you call this from a precompile context, ensure that
// you use the existing stateDB.
func (k Keeper) CallEVMWithData(ctx sdk.Context, stateDB *statedb.StateDB, from common.Address, contract *common.Address, data []byte, commit bool, callFromPrecompile bool, gasCap *big.Int) (*types.MsgEthereumTxResponse, error) {
	nonce, err := k.accountKeeper.GetSequence(ctx, from.Bytes())
	if err != nil {
		return nil, err
	}

	// Bound the EVM gas limit by the caller-provided gasCap when set, falling
	// back to DefaultGasCap. This prevents callers (e.g. CosmWasm query bindings)
	// from triggering EVM execution larger than their remaining gas budget.
	gasLimit := config.DefaultGasCap
	if gasCap != nil && gasCap.IsUint64() {
		if capped := gasCap.Uint64(); capped < gasLimit {
			gasLimit = capped
		}
	}

	msg := core.Message{
		From:       from,
		To:         contract,
		Nonce:      nonce,
		Value:      big.NewInt(0),
		GasLimit:   gasLimit,
		GasPrice:   big.NewInt(0),
		GasTipCap:  big.NewInt(0),
		GasFeeCap:  big.NewInt(0),
		Data:       data,
		AccessList: ethtypes.AccessList{},
	}

	res, err := k.ApplyMessage(ctx, stateDB, msg, nil, commit, callFromPrecompile, true)
	if err != nil {
		return nil, err
	}

	if res.Failed() {
		k.ResetGasMeterAndConsumeGas(ctx, ctx.GasMeter().Limit())
		return res, errorsmod.Wrap(types.ErrVMExecution, res.VmError)
	}

	// Charge the SDK gas meter for the actual pre-refund EVM work (MaxUsedGas)
	// rather than the post-refund GasUsed. Internal calls run with a full refund
	// and no minimum-gas floor, so GasUsed can fall far below the real compute
	// performed by validators; billing MaxUsedGas removes that undercharge.
	// Precompile sub-calls keep their existing accounting.
	chargedGas := res.GasUsed
	if !callFromPrecompile && res.MaxUsedGas > chargedGas {
		chargedGas = res.MaxUsedGas
	}

	ctx.GasMeter().ConsumeGas(chargedGas, "apply evm message")

	return res, nil
}
