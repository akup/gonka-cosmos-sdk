package keeper

import (
	"context"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// InitGenesis sets the pool and parameters for the provided keeper. For each
// validator in data, it sets that validator in the keeper along with manually
// setting the indexes. In addition, it also sets any delegations found in
// data. Finally, it updates the bonded validators.
// Returns final validator set after applying all declaration and delegations
func (k Keeper) InitGenesis(ctx context.Context, data *types.GenesisState) (res []abci.ValidatorUpdate) {
	// check if the module account exists
	moduleAcc := k.authKeeper.GetModuleAccount(ctx, types.ModuleName)
	if moduleAcc == nil {
		panic(fmt.Sprintf("%s module account has not been set", types.ModuleName))
	}

	if err := k.SetParams(ctx, data.Params); err != nil {
		panic(err)
	}

	if err := k.SetLastTotalPower(ctx, data.LastTotalPower); err != nil {
		panic(err)
	}

	var computeResults []ComputeResult
	for _, validator := range data.Validators {
		pk, err := validator.ConsPubKey()
		if err != nil {
			panic(fmt.Errorf("invalid pubkey in genesis state for validator %s: %w", validator.OperatorAddress, err))
		}

		computeResults = append(computeResults, ComputeResult{
			Power:           validator.Tokens.Int64(),
			ValidatorPubKey: pk,
			OperatorAddress: validator.OperatorAddress,
		})
	}

	if _, err := k.SetComputeValidators(ctx, computeResults); err != nil {
		panic(fmt.Errorf("failed to set compute validators during genesis: %w", err))
	}

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
