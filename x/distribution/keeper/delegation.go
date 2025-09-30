package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// initialize starting info for a new delegation
func (k Keeper) initializeDelegation(ctx context.Context, val sdk.ValAddress, del sdk.AccAddress) error {
	validator, err := k.stakingKeeper.Validator(ctx, val)
	if err != nil {
		return err
	}
	return k.SetDelegatorStartingInfo(
		ctx, val, del, types.NewDelegatorStartingInfo(0, validator.GetTokens().ToLegacyDec(), 0))
}

// calculate the total rewards accrued by a delegation
func (k Keeper) CalculateDelegationRewards(ctx context.Context, val stakingtypes.ValidatorI, del stakingtypes.DelegationI, endingPeriod uint64) (rewards sdk.DecCoins, err error) {
	return sdk.DecCoins{}, nil
}

func (k Keeper) withdrawDelegationRewards(ctx context.Context, val stakingtypes.ValidatorI, del stakingtypes.DelegationI) (sdk.Coins, error) {
	return sdk.Coins{}, nil
}
