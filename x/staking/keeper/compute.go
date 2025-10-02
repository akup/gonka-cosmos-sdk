package keeper

import (
	"context"
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

	resultsMap := make(map[string]ComputeResult)
	for _, res := range computeResults {
		if res.ValidatorPubKey == nil {
			continue
		}
		resultsMap[res.OperatorAddress] = res
	}

	currentValidators, err := k.GetAllValidators(ctx)
	if err != nil {
		logger.Error("failed to get all validators", "error", err)
		return nil, err
	}

	currentValMap := make(map[string]types.Validator)
	for _, val := range currentValidators {
		currentValMap[val.OperatorAddress] = val
	}

	for pubKeyAddr, result := range resultsMap {
		val, found := currentValMap[pubKeyAddr]

		power := math.NewInt(result.Power)
		if power.IsNegative() {
			logger.Info("skipping validator with negative power", "pubkey", result.ValidatorPubKey.Address())
			continue
		}

		if !found {
			if power.IsZero() {
				continue
			}
			logger.Info("creating new validator", "pubkey", result.ValidatorPubKey.Address(), "power", power)
			if err := k.createValidatorImmediate(ctx, result.OperatorAddress, result.ValidatorPubKey, power); err != nil {
				logger.Error("failed to create validator", "pubkey", result.ValidatorPubKey.Address(), "error", err)
			}
		} else {
			if val.Tokens == power && val.IsBonded() && !val.Jailed {
				continue
			}

			if power.IsZero() {
				// Power is zero, so remove the validator
				logger.Info("removing validator with zero power", "operator", val.OperatorAddress)
				if err := k.removeValidator(ctx, val); err != nil {
					logger.Error("failed to remove validator", "operator", val.OperatorAddress, "error", err)
				}
			} else {
				logger.Info("updating validator power", "operator", val.OperatorAddress, "new_power", power)
				if err := k.updateValidatorPower(ctx, val, power); err != nil {
					logger.Error("failed to update validator power", "operator", val.OperatorAddress, "error", err)
				}
			}
		}
	}

	// Remove validators that are no longer in the compute results
	for consAddrStr, val := range currentValMap {
		if _, exists := resultsMap[consAddrStr]; !exists {
			logger.Info("removing validator no longer in compute results", "operator", val.OperatorAddress)
			if err := k.removeValidator(ctx, val); err != nil {
				logger.Error("failed to remove stale validator", "operator", val.OperatorAddress, "error", err)
			}
		}
	}

	return k.GetAllValidators(ctx)
}

// createValidatorImmediate creates and bonds a new validator.
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

	// Create self-delegation (needed for module compatibility even if distribution is disabled)
	delegator := sdk.AccAddress(valAddr)
	delegation := types.NewDelegation(delegator.String(), operatorAddress, math.LegacyNewDecFromInt(power))
	if err := k.SetDelegation(ctx, delegation); err != nil {
		logger.Error("failed to set delegation", "delegator", delegator.String(), "validator", operatorAddress, "error", err)
		return err
	}

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

	consensusPower := validator.ConsensusPower(k.PowerReduction(ctx))
	if err := k.SetLastValidatorPower(ctx, valAddr, consensusPower); err != nil {
		logger.Error("failed to set last validator power", "validator", operatorAddress, "power", consensusPower, "error", err)
		return err
	}

	return nil
}

// updateValidatorPower updates an existing validator's power.
func (k Keeper) updateValidatorPower(ctx context.Context, validator types.Validator, newPower math.Int) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(sdkCtx)

	if err := k.DeleteValidatorByPowerIndex(ctx, validator); err != nil {
		logger.Error("failed to delete validator by power index", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	oldStatus := validator.Status
	oldJailed := validator.Jailed

	validator.Tokens = newPower
	validator.DelegatorShares = math.LegacyNewDecFromInt(newPower)
	validator.Status = types.Bonded // Ensure validator is bonded
	validator.Jailed = false        // Unjail if it was jailed

	if err := k.SetValidator(ctx, validator); err != nil {
		logger.Error("failed to set validator for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}
	if err := k.SetValidatorByConsAddr(ctx, validator); err != nil {
		logger.Error("failed to set validator by cons addr for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}
	if err := k.SetValidatorByPowerIndex(ctx, validator); err != nil {
		logger.Error("failed to set validator by power index for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.OperatorAddress)
	if err != nil {
		logger.Error("failed to convert operator address for delegation update", "address", validator.OperatorAddress, "error", err)
		return err
	}
	delegator := sdk.AccAddress(valAddr)
	delegation, err := k.GetDelegation(ctx, delegator, valAddr)
	if err != nil {
		delegation = types.NewDelegation(delegator.String(), validator.OperatorAddress, math.LegacyNewDecFromInt(newPower))
	} else {
		delegation.Shares = math.LegacyNewDecFromInt(newPower)
	}
	if err := k.SetDelegation(ctx, delegation); err != nil {
		logger.Error("failed to set delegation for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	statusChanged := oldStatus != types.Bonded && validator.Status == types.Bonded
	wasUnjailed := oldJailed && !validator.Jailed
	if statusChanged || wasUnjailed {
		consAddr, err := validator.GetConsAddr()
		if err != nil {
			logger.Error("failed to get validator cons addr for bonded hook", "validator", validator.OperatorAddress, "error", err)
			return err
		}
		if err := k.Hooks().AfterValidatorBonded(ctx, consAddr, valAddr); err != nil {
			logger.Error("failed to call AfterValidatorBonded hook for power update", "validator", validator.OperatorAddress, "error", err)
			return err
		}
	}

	if err := k.Hooks().AfterDelegationModified(ctx, delegator, valAddr); err != nil {
		logger.Error("failed to call AfterDelegationModified hook for power update", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	consensusPower := validator.ConsensusPower(k.PowerReduction(ctx))
	if err := k.SetLastValidatorPower(ctx, valAddr, consensusPower); err != nil {
		logger.Error("failed to set last validator power for update", "validator", validator.OperatorAddress, "power", consensusPower, "error", err)
		return err
	}

	return nil
}

// removeValidator sets validator power to zero.
// The validator remains in storage and will be transitioned to unbonding
// by ApplyAndReturnValidatorSetUpdates, then deleted after unbonding period.
func (k Keeper) removeValidator(ctx context.Context, validator types.Validator) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(sdkCtx)

	// Remove from old power index before changing power
	if err := k.DeleteValidatorByPowerIndex(ctx, validator); err != nil {
		logger.Error("failed to delete validator by power index for removal", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	// Set power to 0 but keep status as Bonded
	// ApplyAndReturnValidatorSetUpdates will transition it from Bonded -> Unbonding -> deleted
	validator.Tokens = math.ZeroInt()
	validator.DelegatorShares = math.LegacyZeroDec()
	// Keep validator.Status as-is (should be Bonded) for proper state transition

	// Save the updated validator
	if err := k.SetValidator(ctx, validator); err != nil {
		logger.Error("failed to set validator with zero power", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	// Re-add to power index with zero power
	if err := k.SetValidatorByPowerIndex(ctx, validator); err != nil {
		logger.Error("failed to set validator by power index with zero power", "validator", validator.OperatorAddress, "error", err)
		return err
	}

	// Set delegation to zero shares (this signals for cleanup in UnbondAllMatureValidators)
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.OperatorAddress)
	if err != nil {
		logger.Error("failed to convert operator address for delegation removal", "address", validator.OperatorAddress, "error", err)
		return err
	}
	delegator := sdk.AccAddress(valAddr)
	delegation, err := k.GetDelegation(ctx, delegator, valAddr)
	if err == nil {
		delegation.Shares = math.LegacyZeroDec()
		if err := k.SetDelegation(ctx, delegation); err != nil {
			logger.Error("failed to set delegation to zero", "validator", validator.OperatorAddress, "error", err)
			return err
		}
	}

	return nil
}
