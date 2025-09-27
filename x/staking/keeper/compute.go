package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/math"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// ComputeResult defines the structure for validator power updates.
type ComputeResult struct {
	Power           int64
	ValidatorPubKey cryptotypes.PubKey
	OperatorAddress string
}

// SetComputeValidators is the main entry point for updating the validator set.
// It synchronizes the state with the provided list of compute results.
func (k Keeper) SetComputeValidators(ctx context.Context, computeResults []ComputeResult) ([]types.Validator, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(sdkCtx)

	// Build maps for efficient lookups
	resultsMap := make(map[string]ComputeResult)
	for _, res := range computeResults {
		if res.ValidatorPubKey == nil {
			continue // Skip invalid results
		}
		resultsMap[string(res.ValidatorPubKey.Address())] = res
	}

	currentValidators, err := k.GetAllValidators(ctx)
	if err != nil {
		logger.Error("failed to get all validators", "error", err)
		return nil, err
	}

	currentValMap := make(map[string]types.Validator)
	for _, val := range currentValidators {
		addr, err := val.GetConsAddr()
		if err != nil {
			logger.Error("failed to get validator consensus address", "validator", val.OperatorAddress, "error", err)
			continue
		}
		currentValMap[string(addr)] = val
	}

	// Create new validators and update existing ones
	for pubKeyAddr, result := range resultsMap {
		val, found := currentValMap[pubKeyAddr]

		power := math.NewInt(result.Power)
		if power.IsNegative() {
			logger.Info("skipping validator with negative power", "pubkey", result.ValidatorPubKey.Address())
			continue
		}

		if !found {
			// This is a new validator
			if power.IsZero() {
				continue // Don't create a validator with zero power
			}
			logger.Info("creating new validator", "pubkey", result.ValidatorPubKey.Address(), "power", power)
			if err := k.createValidatorImmediate(ctx, result.OperatorAddress, result.ValidatorPubKey, power); err != nil {
				logger.Error("failed to create validator", "pubkey", result.ValidatorPubKey.Address(), "error", err)
			}
		} else {
			// This is an existing validator, check for updates
			if val.GetConsensusPower(k.PowerReduction(ctx)) == power.Int64() && val.IsBonded() && !val.Jailed {
				continue // No change needed
			}

			if power.IsZero() {
				// Power is zero, so remove the validator
				logger.Info("removing validator with zero power", "operator", val.OperatorAddress)
				if err := k.removeValidatorImmediate(ctx, val); err != nil {
					logger.Error("failed to remove validator", "operator", val.OperatorAddress, "error", err)
				}
			} else {
				// Power has changed, update the validator
				logger.Info("updating validator power", "operator", val.OperatorAddress, "new_power", power)
				if err := k.updateValidatorPowerImmediate(ctx, val, power); err != nil {
					logger.Error("failed to update validator power", "operator", val.OperatorAddress, "error", err)
				}
			}
		}
	}

	// Remove validators that are no longer in the compute results
	for consAddrStr, val := range currentValMap {
		if _, exists := resultsMap[consAddrStr]; !exists {
			logger.Info("removing validator no longer in compute results", "operator", val.OperatorAddress)
			if err := k.removeValidatorImmediate(ctx, val); err != nil {
				logger.Error("failed to remove stale validator", "operator", val.OperatorAddress, "error", err)
			}
		}
	}

	return k.GetAllValidators(ctx)
}

// createValidatorImmediate creates, bonds, and self-delegates for a new validator.
// The power parameter represents both the desired consensus power AND the token amount (1:1 mapping).
func (k Keeper) createValidatorImmediate(ctx context.Context, operatorAddress string, pubkey cryptotypes.PubKey, power math.Int) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(sdkCtx)

	valAddr, err := k.ValidatorAddressCodec().StringToBytes(operatorAddress)
	if err != nil {
		logger.Error("failed to convert operator address to bytes", "address", operatorAddress, "error", err)
		return fmt.Errorf("invalid operator address %s: %w", operatorAddress, err)
	}

	// Create the validator object
	validator, err := types.NewValidator(operatorAddress, pubkey, types.Description{Moniker: operatorAddress})
	if err != nil {
		logger.Error("failed to create new validator", "operator", operatorAddress, "error", err)
		return err
	}
	validator.Status = types.Bonded // Set as bonded immediately

	// Direct 1:1 mapping: tokens = consensus power
	validator.Tokens = power
	validator.DelegatorShares = math.LegacyNewDecFromInt(power)

	// Save validator to store
	if err := k.SetValidator(ctx, validator); err != nil {
		logger.Error("failed to set validator", "operator", operatorAddress, "error", err)
		return err
	}
	if err := k.SetValidatorByConsAddr(ctx, validator); err != nil {
		logger.Error("failed to set validator by cons addr", "operator", operatorAddress, "error", err)
		return err
	}
	if err := k.SetValidatorByPowerIndex(ctx, validator); err != nil {
		logger.Error("failed to set validator by power index", "operator", operatorAddress, "error", err)
		return err
	}

	// Create the 1:1 self-delegation
	delegator := sdk.AccAddress(valAddr)
	delegation := types.NewDelegation(delegator.String(), operatorAddress, math.LegacyNewDecFromInt(power))
	if err := k.SetDelegation(ctx, delegation); err != nil {
		logger.Error("failed to set delegation", "delegator", delegator.String(), "validator", operatorAddress, "error", err)
		return err
	}

	// Call hooks
	if err := k.Hooks().AfterValidatorCreated(ctx, valAddr); err != nil {
		logger.Error("failed to call AfterValidatorCreated hook", "validator", operatorAddress, "error", err)
		return err
	}
	consAddr, err := validator.GetConsAddr()
	if err != nil {
		logger.Error("failed to get validator cons addr", "validator", operatorAddress, "error", err)
		return err
	}
	if err := k.Hooks().AfterValidatorBonded(ctx, consAddr, valAddr); err != nil {
		logger.Error("failed to call AfterValidatorBonded hook", "validator", operatorAddress, "error", err)
		return err
	}
	if err := k.Hooks().AfterDelegationModified(ctx, delegator, valAddr); err != nil {
		logger.Error("failed to call AfterDelegationModified hook", "delegator", delegator.String(), "validator", operatorAddress, "error", err)
		return err
	}

	return nil
}

// updateValidatorPowerImmediate updates an existing validator's power and self-delegation.
func (k Keeper) updateValidatorPowerImmediate(ctx context.Context, validator types.Validator, newPower math.Int) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(sdkCtx)

	// First, remove the validator from the old power index
	if err := k.DeleteValidatorByPowerIndex(ctx, validator); err != nil {
		logger.Error("failed to delete validator by power index", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	// Direct 1:1 mapping: tokens = consensus power
	validator.Tokens = newPower
	validator.DelegatorShares = math.LegacyNewDecFromInt(newPower)
	validator.Status = types.Bonded // Ensure validator is bonded
	validator.Jailed = false        // Unjail if it was jailed

	// Save the updated validator
	if err := k.SetValidator(ctx, validator); err != nil {
		logger.Error("failed to set validator for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}
	// Re-add to the power index with new power
	if err := k.SetValidatorByPowerIndex(ctx, validator); err != nil {
		logger.Error("failed to set validator by power index for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	// Update the self-delegation
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.OperatorAddress)
	if err != nil {
		logger.Error("failed to convert operator address to bytes for power update", "address", validator.OperatorAddress, "error", err)
		return err
	}
	delegator := sdk.AccAddress(valAddr)
	delegation, err := k.GetDelegation(ctx, delegator, valAddr)
	if err != nil {
		logger.Info("delegation not found for validator, creating new one", "validator", validator.OperatorAddress)
		// If delegation doesn't exist, create it
		delegation = types.NewDelegation(delegator.String(), validator.OperatorAddress, math.LegacyNewDecFromInt(newPower))
	} else {
		delegation.Shares = math.LegacyNewDecFromInt(newPower)
	}

	if err := k.SetDelegation(ctx, delegation); err != nil {
		logger.Error("failed to set delegation for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	// Call hook
	if err := k.Hooks().AfterDelegationModified(ctx, delegator, valAddr); err != nil {
		logger.Error("failed to call AfterDelegationModified hook for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	return nil
}

// removeValidatorImmediate removes a validator and its self-delegation.
func (k Keeper) removeValidatorImmediate(ctx context.Context, validator types.Validator) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(sdkCtx)

	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.OperatorAddress)
	if err != nil {
		logger.Error("failed to convert operator address to bytes for removal", "address", validator.OperatorAddress, "error", err)
		return err
	}

	consAddr, err := validator.GetConsAddr()
	if err != nil {
		logger.Error("failed to get cons addr for removal", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	// 1. Remove self-delegation
	delegator := sdk.AccAddress(valAddr)
	delegation, err := k.GetDelegation(ctx, delegator, valAddr)
	if err == nil {
		if err := k.RemoveDelegation(ctx, delegation); err != nil {
			// Log error but continue, as the delegation might already be gone
			logger.Error("failed to remove self-delegation", "validator", validator.OperatorAddress, "error", err)
		}
	} else if !errors.Is(err, types.ErrNoDelegation) {
		logger.Error("unexpected error getting delegation for removal", "validator", validator.OperatorAddress, "error", err)
	}

	// 2. Directly remove validator and all its indexes
	if err := k.DeleteValidatorByPowerIndex(ctx, validator); err != nil {
		logger.Error("failed to delete validator by power index for removal", "validator", validator.OperatorAddress, "error", err)
		return err
	}
	store := k.storeService.OpenKVStore(ctx)
	if err := store.Delete(types.GetValidatorByConsAddrKey(consAddr)); err != nil {
		logger.Error("failed to delete validator by cons addr for removal", "validator", validator.OperatorAddress, "error", err)
		return err
	}
	if err := store.Delete(types.GetValidatorKey(valAddr)); err != nil {
		logger.Error("failed to delete validator from store", "validator", validator.OperatorAddress, "error", err)
		return fmt.Errorf("failed to delete validator from store: %w", err)
	}

	// 3. Call hook
	if err := k.Hooks().AfterValidatorRemoved(ctx, consAddr, valAddr); err != nil {
		logger.Error("failed to call AfterValidatorRemoved hook", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	return nil
}
