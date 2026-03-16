// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.0;

import "../../interfaces/ISlashingManager.sol";
import "../../interfaces/IDelegationManager.sol";

abstract contract SlashingManagerStorage is ISlashingManager {
    uint256 public MIN_WITHDRAWAL_AMOUNT;

    IDelegationManager public delegationManager;

    address public slasherAddress;

    address public slashingRecipient;

    uint256 public totalProcessedSlashingAmount;

    mapping(address => bool) public isOperatorJail;

    mapping(address => uint256) public slashingOperatorShares;

    mapping(address => uint256) public slashingStakerShares;

    uint256[100] private __gap;
}
