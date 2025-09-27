package keeper

import (
	"context"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// InitGenesis sets the pool and parameters for the provided keeper using compute-based validation.
// It converts genesis validators to ComputeResults and uses SetComputeValidators for initialization.
// Returns final validator set after applying all declaration and delegations
func (k Keeper) InitGenesis(ctx context.Context, data *types.GenesisState) (res []abci.ValidatorUpdate) {
	// We need to pretend to be "n blocks before genesis", where "n" is the
	// validator update delay, so that e.g. slashing periods are correctly
	// initialized for the validator set e.g. with a one-block offset - the
	// first TM block is at height 1, so state updates applied from
	// genesis.json are in block 0.
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sdkCtx = sdkCtx.WithBlockHeight(1 - sdk.ValidatorUpdateDelay)
	ctx = sdkCtx

	if err := k.SetParams(ctx, data.Params); err != nil {
		panic(err)
	}

	if err := k.SetLastTotalPower(ctx, data.LastTotalPower); err != nil {
		panic(err)
	}

	// Create a slice of ComputeResult from the genesis validators
	// In compute-based staking, we treat the token amount as the direct consensus power
	var computeResults []ComputeResult
	for _, validator := range data.Validators {
		var pk cryptotypes.PubKey
		pk, err := validator.ConsPubKey()
		if err != nil {
			panic(fmt.Errorf("invalid pubkey in genesis state for validator %s: %w", validator.OperatorAddress, err))
		}

		// Use the token amount directly as consensus power (1:1 mapping)
		computeResults = append(computeResults, ComputeResult{
			Power:           validator.Tokens.Int64(),
			ValidatorPubKey: pk,
			OperatorAddress: validator.OperatorAddress,
		})
	}

	// Use our compute-based function to initialize the entire validator set
	if _, err := k.SetComputeValidators(ctx, computeResults); err != nil {
		panic(fmt.Errorf("failed to set compute validators during genesis: %w", err))
	}

	// Set up module accounts (simplified version of the original logic)
	bondedPool := k.GetBondedPool(ctx)
	if bondedPool == nil {
		panic(fmt.Sprintf("%s module account has not been set", types.BondedPoolName))
	}

	bondedBalance := k.bankKeeper.GetAllBalances(ctx, bondedPool.GetAddress())
	if bondedBalance.IsZero() {
		k.authKeeper.SetModuleAccount(ctx, bondedPool)
	}

	notBondedPool := k.GetNotBondedPool(ctx)
	if notBondedPool == nil {
		panic(fmt.Sprintf("%s module account has not been set", types.NotBondedPoolName))
	}

	notBondedBalance := k.bankKeeper.GetAllBalances(ctx, notBondedPool.GetAddress())
	if notBondedBalance.IsZero() {
		k.authKeeper.SetModuleAccount(ctx, notBondedPool)
	}

	// Set last validator powers for each validator
	for _, val := range data.Validators {
		valAddr, err := k.ValidatorAddressCodec().StringToBytes(val.GetOperator())
		if err != nil {
			panic(err)
		}

		// In compute-based staking, consensus power equals token amount
		consensusPower := val.Tokens.Int64()
		if err := k.SetLastValidatorPower(ctx, valAddr, consensusPower); err != nil {
			panic(err)
		}
	}

	// Apply validator set updates
	var err error
	res, err = k.ApplyAndReturnValidatorSetUpdates(ctx)
	if err != nil {
		panic(err)
	}

	return res
}

// ExportGenesis returns a GenesisState for a given context and keeper. The
// GenesisState will contain the pool, params, validators, and bonds found in
// the keeper.
func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	var unbondingDelegations []types.UnbondingDelegation

	err := k.IterateUnbondingDelegations(ctx, func(_ int64, ubd types.UnbondingDelegation) (stop bool) {
		unbondingDelegations = append(unbondingDelegations, ubd)
		return false
	})
	if err != nil {
		panic(err)
	}

	var redelegations []types.Redelegation

	err = k.IterateRedelegations(ctx, func(_ int64, red types.Redelegation) (stop bool) {
		redelegations = append(redelegations, red)
		return false
	})
	if err != nil {
		panic(err)
	}

	var lastValidatorPowers []types.LastValidatorPower

	err = k.IterateLastValidatorPowers(ctx, func(addr sdk.ValAddress, power int64) (stop bool) {
		addrStr, err := k.validatorAddressCodec.BytesToString(addr)
		if err != nil {
			panic(err)
		}
		lastValidatorPowers = append(lastValidatorPowers, types.LastValidatorPower{Address: addrStr, Power: power})
		return false
	})
	if err != nil {
		panic(err)
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		panic(err)
	}

	totalPower, err := k.GetLastTotalPower(ctx)
	if err != nil {
		panic(err)
	}

	allDelegations, err := k.GetAllDelegations(ctx)
	if err != nil {
		panic(err)
	}

	allValidators, err := k.GetAllValidators(ctx)
	if err != nil {
		panic(err)
	}

	return &types.GenesisState{
		Params:               params,
		LastTotalPower:       totalPower,
		LastValidatorPowers:  lastValidatorPowers,
		Validators:           allValidators,
		Delegations:          allDelegations,
		UnbondingDelegations: unbondingDelegations,
		Redelegations:        redelegations,
		Exported:             true,
	}
}
