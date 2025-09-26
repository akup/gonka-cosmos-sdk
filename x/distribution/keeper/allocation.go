package keeper

import (
	"context"

	abci "github.com/cometbft/cometbft/abci/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// AllocateTokens performs reward and fee distribution to all validators based
// on the F1 fee distribution specification.
func (k Keeper) AllocateTokens(ctx context.Context, totalPreviousPower int64, bondedVotes []abci.VoteInfo) error {
	// Return immediately to prevent all validator reward calculations and distributions.
	return nil
}

// AllocateTokensToValidator allocate tokens to a particular validator,
// splitting according to commission.
func (k Keeper) AllocateTokensToValidator(ctx context.Context, val stakingtypes.ValidatorI, tokens sdk.DecCoins) error {
	// split tokens between validator and delegators according to commission
	commission := tokens.MulDec(val.GetCommission())
	shared := tokens.Sub(commission)

	valBz, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
	if err != nil {
		return err
	}

	// update current commission
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeCommission,
			sdk.NewAttribute(sdk.AttributeKeyAmount, commission.String()),
			sdk.NewAttribute(types.AttributeKeyValidator, val.GetOperator()),
		),
	)
	currentCommission, err := k.GetValidatorAccumulatedCommission(ctx, valBz)
	if err != nil {
		return err
	}

	currentCommission.Commission = currentCommission.Commission.Add(commission...)
	err = k.SetValidatorAccumulatedCommission(ctx, valBz, currentCommission)
	if err != nil {
		return err
	}

	// update current rewards
	currentRewards, err := k.GetValidatorCurrentRewards(ctx, valBz)
	if err != nil {
		return err
	}

	currentRewards.Rewards = currentRewards.Rewards.Add(shared...)
	err = k.SetValidatorCurrentRewards(ctx, valBz, currentRewards)
	if err != nil {
		return err
	}

	// update outstanding rewards
	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeRewards,
			sdk.NewAttribute(sdk.AttributeKeyAmount, tokens.String()),
			sdk.NewAttribute(types.AttributeKeyValidator, val.GetOperator()),
		),
	)

	outstanding, err := k.GetValidatorOutstandingRewards(ctx, valBz)
	if err != nil {
		return err
	}

	outstanding.Rewards = outstanding.Rewards.Add(tokens...)
	return k.SetValidatorOutstandingRewards(ctx, valBz, outstanding)
}

// sendCommunityPoolToExternalPool does the following:
//
//	truncate the community pool value (DecCoins) to sdk.Coins
//	distribute from the distribution module account to the external community pool account
//	update the bookkept value in x/distribution
func (k Keeper) sendCommunityPoolToExternalPool(ctx sdk.Context) error {
	feePool, err := k.FeePool.Get(ctx)
	if err != nil {
		return err
	}

	if feePool.CommunityPool.IsZero() {
		return nil
	}

	amt, remaining := feePool.CommunityPool.TruncateDecimal()
	ctx.Logger().Debug(
		"sending distribution community pool amount to external pool pool",
		"pool", k.externalCommunityPool.GetCommunityPoolModule(),
		"amount", amt.String(),
		"remaining", remaining.String(),
	)
	if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, types.ModuleName, k.externalCommunityPool.GetCommunityPoolModule(), amt); err != nil {
		return err
	}

	return k.FeePool.Set(ctx, types.FeePool{CommunityPool: remaining})
}
