package keeper

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	abci "github.com/cometbft/cometbft/abci/types"
	gogotypes "github.com/cosmos/gogoproto/types"

	"cosmossdk.io/core/address"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// BlockValidatorUpdates calculates the ValidatorUpdates for the current block
// Called in each EndBlock
func (k Keeper) BlockValidatorUpdates(ctx context.Context) ([]abci.ValidatorUpdate, error) {
	// Calculate validator set changes.
	//
	// NOTE: ApplyAndReturnValidatorSetUpdates has to come before
	// UnbondAllMatureValidatorQueue.
	// This fixes a bug when the unbonding period is instant (is the case in
	// some of the tests). The test expected the validator to be completely
	// unbonded after the Endblocker (go from Bonded -> Unbonding during
	// ApplyAndReturnValidatorSetUpdates and then Unbonding -> Unbonded during
	// UnbondAllMatureValidatorQueue).
	validatorUpdates, err := k.ApplyAndReturnValidatorSetUpdates(ctx)
	if err != nil {
		return nil, err
	}

	// unbond all mature validators from the unbonding queue
	err = k.UnbondAllMatureValidators(ctx)
	if err != nil {
		return nil, err
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	// Remove all mature unbonding delegations from the ubd queue.
	matureUnbonds, err := k.DequeueAllMatureUBDQueue(ctx, sdkCtx.BlockHeader().Time)
	if err != nil {
		return nil, err
	}

	for _, dvPair := range matureUnbonds {
		addr, err := k.validatorAddressCodec.StringToBytes(dvPair.ValidatorAddress)
		if err != nil {
			return nil, err
		}
		delegatorAddress, err := k.authKeeper.AddressCodec().StringToBytes(dvPair.DelegatorAddress)
		if err != nil {
			return nil, err
		}

		balances, err := k.CompleteUnbonding(ctx, delegatorAddress, addr)
		if err != nil {
			continue
		}

		sdkCtx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeCompleteUnbonding,
				sdk.NewAttribute(sdk.AttributeKeyAmount, balances.String()),
				sdk.NewAttribute(types.AttributeKeyValidator, dvPair.ValidatorAddress),
				sdk.NewAttribute(types.AttributeKeyDelegator, dvPair.DelegatorAddress),
			),
		)
	}

	// Remove all mature redelegations from the red queue.
	matureRedelegations, err := k.DequeueAllMatureRedelegationQueue(ctx, sdkCtx.BlockHeader().Time)
	if err != nil {
		return nil, err
	}

	for _, dvvTriplet := range matureRedelegations {
		valSrcAddr, err := k.validatorAddressCodec.StringToBytes(dvvTriplet.ValidatorSrcAddress)
		if err != nil {
			return nil, err
		}
		valDstAddr, err := k.validatorAddressCodec.StringToBytes(dvvTriplet.ValidatorDstAddress)
		if err != nil {
			return nil, err
		}
		delegatorAddress, err := k.authKeeper.AddressCodec().StringToBytes(dvvTriplet.DelegatorAddress)
		if err != nil {
			return nil, err
		}

		balances, err := k.CompleteRedelegation(
			ctx,
			delegatorAddress,
			valSrcAddr,
			valDstAddr,
		)
		if err != nil {
			continue
		}

		sdkCtx.EventManager().EmitEvent(
			sdk.NewEvent(
				types.EventTypeCompleteRedelegation,
				sdk.NewAttribute(sdk.AttributeKeyAmount, balances.String()),
				sdk.NewAttribute(types.AttributeKeyDelegator, dvvTriplet.DelegatorAddress),
				sdk.NewAttribute(types.AttributeKeySrcValidator, dvvTriplet.ValidatorSrcAddress),
				sdk.NewAttribute(types.AttributeKeyDstValidator, dvvTriplet.ValidatorDstAddress),
			),
		)
	}

	return validatorUpdates, nil
}

// ApplyAndReturnValidatorSetUpdates applies and return accumulated updates to the bonded validator set. Also,
// * Updates the active valset as keyed by LastValidatorPowerKey.
// * Updates the total power as keyed by LastTotalPowerKey.
// * Updates validator status' according to updated powers.
// * Updates the fee pool bonded vs not-bonded tokens.
// * Updates relevant indices.
// It gets called once after genesis, another time maybe after genesis transactions,
// then once at every EndBlock.
//
// CONTRACT: Only validators with non-zero power or zero-power that were bonded
// at the previous block height or were removed from the validator set entirely
// are returned to CometBFT.
func (k Keeper) ApplyAndReturnValidatorSetUpdates(ctx context.Context) (updates []abci.ValidatorUpdate, err error) {
	k.Logger(ctx).Info("applying proof-of-compute validator set updates")
	powerReduction := k.PowerReduction(ctx)
	totalPower := math.ZeroInt()

	// Retrieve the last validator set that CometBFT knows about.
	last, err := k.getLastValidatorsByAddr(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get last validator set: %w", err)
	}

	// Retrieve the current validator set from the state.
	currentValidators, err := k.GetAllValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all validators: %w", err)
	}

	for _, validator := range currentValidators {
		// only bonded validators are considered part of the active set
		if !validator.IsBonded() {
			continue
		}

		valAddr, err := k.validatorAddressCodec.StringToBytes(validator.GetOperator())
		if err != nil {
			return nil, fmt.Errorf("failed to get validator operator address: %w", err)
		}
		valAddrStr := string(valAddr)

		newPower := validator.ConsensusPower(powerReduction)
		totalPower = totalPower.AddRaw(newPower)

		oldPower, found := last[valAddrStr]

		// update the validator set if power has changed or the validator is new
		if !found || oldPower != newPower {
			updates = append(updates, validator.ABCIValidatorUpdate(powerReduction))
			k.Logger(ctx).Info("validator update", "operator", validator.OperatorAddress, "old_power", oldPower, "new_power", newPower)

			if err = k.SetLastValidatorPower(ctx, valAddr, newPower); err != nil {
				return nil, err
			}
		}

		// remove from the `last` map; remaining entries will be validators that are no longer bonded
		delete(last, valAddrStr)
	}

	// Any validators remaining in `last` were bonded in the previous block but are not in the current bonded set.
	// We must inform CometBFT to remove them.
	noLongerBonded, err := sortNoLongerBonded(last, k.validatorAddressCodec)
	if err != nil {
		return nil, err
	}

	for _, valAddrBytes := range noLongerBonded {
		// To create the removal update, we need the validator's public key.
		// We attempt to retrieve the full validator object from the state.
		validator, err := k.GetValidator(ctx, sdk.ValAddress(valAddrBytes))
		if err != nil {
			// IMPORTANT: If this error occurs, it means `removeValidatorImmediate` deleted the validator
			// from the state before this function could create the zero-power update for CometBFT.
			// The `removeValidatorImmediate` function must be modified to ensure the validator record
			// persists until after this update is processed. It should transition the validator to
			// an 'Unbonded' state with zero power instead of deleting it.
			k.Logger(ctx).Error("could not retrieve validator for removal update; consensus may desync", "address", sdk.ValAddress(valAddrBytes).String(), "error", err)
			// We skip this update, which is incorrect but prevents a panic.
			continue
		}

		updates = append(updates, validator.ABCIValidatorUpdateZero())
		k.Logger(ctx).Info("removing validator from consensus set", "operator", validator.OperatorAddress)

		if err = k.DeleteLastValidatorPower(ctx, valAddrBytes); err != nil {
			return nil, err
		}
	}

	// set total power on lookup index if there are any updates
	if len(updates) > 0 {
		if err = k.SetLastTotalPower(ctx, totalPower); err != nil {
			return nil, err
		}
	}

	// set the list of validator updates, which will be returned by EndBlocker
	if err = k.SetValidatorUpdates(ctx, updates); err != nil {
		return nil, err
	}

	return updates, nil
}

// Validator state transitions

func (k Keeper) bondedToUnbonding(ctx context.Context, validator types.Validator) (types.Validator, error) {
	if !validator.IsBonded() {
		return types.Validator{}, fmt.Errorf("bad state transition bondedToUnbonding, validator: %v", validator)
	}

	return k.BeginUnbondingValidator(ctx, validator)
}

func (k Keeper) unbondingToBonded(ctx context.Context, validator types.Validator) (types.Validator, error) {
	if !validator.IsUnbonding() {
		return types.Validator{}, fmt.Errorf("bad state transition unbondingToBonded, validator: %v", validator)
	}

	return k.bondValidator(ctx, validator)
}

func (k Keeper) unbondedToBonded(ctx context.Context, validator types.Validator) (types.Validator, error) {
	if !validator.IsUnbonded() {
		return types.Validator{}, fmt.Errorf("bad state transition unbondedToBonded, validator: %v", validator)
	}

	return k.bondValidator(ctx, validator)
}

// UnbondingToUnbonded switches a validator from unbonding state to unbonded state
func (k Keeper) UnbondingToUnbonded(ctx context.Context, validator types.Validator) (types.Validator, error) {
	if !validator.IsUnbonding() {
		return types.Validator{}, fmt.Errorf("bad state transition unbondingToUnbonded, validator: %v", validator)
	}

	return k.completeUnbondingValidator(ctx, validator)
}

// send a validator to jail
func (k Keeper) jailValidator(ctx context.Context, validator types.Validator) error {
	if validator.Jailed {
		return types.ErrValidatorJailed.Wrapf("cannot jail already jailed validator, validator: %v", validator)
	}

	validator.Jailed = true
	if err := k.SetValidator(ctx, validator); err != nil {
		return err
	}

	return k.DeleteValidatorByPowerIndex(ctx, validator)
}

// remove a validator from jail
func (k Keeper) unjailValidator(ctx context.Context, validator types.Validator) error {
	if !validator.Jailed {
		return fmt.Errorf("cannot unjail already unjailed validator, validator: %v", validator)
	}

	validator.Jailed = false
	if err := k.SetValidator(ctx, validator); err != nil {
		return err
	}

	return k.SetValidatorByPowerIndex(ctx, validator)
}

// perform all the store operations for when a validator status becomes bonded
func (k Keeper) bondValidator(ctx context.Context, validator types.Validator) (types.Validator, error) {
	// delete the validator by power index, as the key will change
	if err := k.DeleteValidatorByPowerIndex(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	validator = validator.UpdateStatus(types.Bonded)

	// save the now bonded validator record to the two referenced stores
	if err := k.SetValidator(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	if err := k.SetValidatorByPowerIndex(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	// delete from queue if present
	if err := k.DeleteValidatorQueue(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	// trigger hook
	consAddr, err := validator.GetConsAddr()
	if err != nil {
		return types.Validator{}, err
	}

	str, err := k.validatorAddressCodec.StringToBytes(validator.GetOperator())
	if err != nil {
		return types.Validator{}, fmt.Errorf("failed to get validator operator address: %w", err)
	}

	if err := k.Hooks().AfterValidatorBonded(ctx, consAddr, str); err != nil {
		return types.Validator{}, err
	}

	return validator, nil
}

// BeginUnbondingValidator performs all the store operations for when a validator begins unbonding
func (k Keeper) BeginUnbondingValidator(ctx context.Context, validator types.Validator) (types.Validator, error) {
	params, err := k.GetParams(ctx)
	if err != nil {
		return types.Validator{}, err
	}

	// delete the validator by power index, as the key will change
	if err = k.DeleteValidatorByPowerIndex(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	// sanity check
	if validator.Status != types.Bonded {
		return types.Validator{}, fmt.Errorf("should not already be unbonded or unbonding, validator: %v", validator)
	}

	id, err := k.IncrementUnbondingID(ctx)
	if err != nil {
		return types.Validator{}, err
	}

	validator = validator.UpdateStatus(types.Unbonding)

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	// set the unbonding completion time and completion height appropriately
	validator.UnbondingTime = sdkCtx.BlockHeader().Time.Add(params.UnbondingTime)
	validator.UnbondingHeight = sdkCtx.BlockHeader().Height

	validator.UnbondingIds = append(validator.UnbondingIds, id)

	// save the now unbonded validator record and power index
	if err = k.SetValidator(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	if err = k.SetValidatorByPowerIndex(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	// Adds to unbonding validator queue
	if err = k.InsertUnbondingValidatorQueue(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	// trigger hook
	consAddr, err := validator.GetConsAddr()
	if err != nil {
		return types.Validator{}, err
	}

	str, err := k.validatorAddressCodec.StringToBytes(validator.GetOperator())
	if err != nil {
		return types.Validator{}, fmt.Errorf("failed to get validator operator address: %w", err)
	}

	if err := k.Hooks().AfterValidatorBeginUnbonding(ctx, consAddr, str); err != nil {
		return types.Validator{}, err
	}

	if err := k.SetValidatorByUnbondingID(ctx, validator, id); err != nil {
		return types.Validator{}, err
	}

	if err := k.Hooks().AfterUnbondingInitiated(ctx, id); err != nil {
		return types.Validator{}, err
	}

	return validator, nil
}

// perform all the store operations for when a validator status becomes unbonded
func (k Keeper) completeUnbondingValidator(ctx context.Context, validator types.Validator) (types.Validator, error) {
	validator = validator.UpdateStatus(types.Unbonded)
	if err := k.SetValidator(ctx, validator); err != nil {
		return types.Validator{}, err
	}

	return validator, nil
}

// map of operator addresses to power
// We use (non bech32) strings here, because we can't have slices as keys: map[[]byte][]byte
type validatorsByAddr map[string]int64

// get the last validator set
func (k Keeper) getLastValidatorsByAddr(ctx context.Context) (validatorsByAddr, error) {
	last := make(validatorsByAddr)

	iterator, err := k.LastValidatorsIterator(ctx)
	if err != nil {
		return nil, err
	}
	defer iterator.Close()

	var intVal gogotypes.Int64Value
	for ; iterator.Valid(); iterator.Next() {
		// extract the validator address from the key (prefix is 1-byte, addrLen is 1-byte)
		valAddrStr := string(types.AddressFromLastValidatorPowerKey(iterator.Key()))
		k.cdc.MustUnmarshal(iterator.Value(), &intVal)
		last[valAddrStr] = intVal.GetValue()
	}

	return last, nil
}

// given a map of remaining validators to previous bonded power
// returns the list of validators to be unbonded, sorted by operator address
func sortNoLongerBonded(last validatorsByAddr, ac address.Codec) ([][]byte, error) {
	// sort the map keys for determinism
	noLongerBonded := make([][]byte, len(last))
	index := 0

	for valAddrStr := range last {
		valAddrBytes := []byte(valAddrStr)
		noLongerBonded[index] = valAddrBytes
		index++
	}
	// sorted by address - order doesn't matter
	sort.SliceStable(noLongerBonded, func(i, j int) bool {
		// -1 means strictly less than
		return bytes.Compare(noLongerBonded[i], noLongerBonded[j]) == -1
	})

	return noLongerBonded, nil
}
