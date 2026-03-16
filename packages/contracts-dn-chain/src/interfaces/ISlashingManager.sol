// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.0;

interface ISlashingManager {
    event IsJail(address indexed operator, bool isJail);

    event SlashingOperatorStakingShares(
        address indexed operator,
        uint256 shares
    );

    event SlashedShareDistributed(
        address indexed operator,
        address indexed staker,
        uint256 amount
    );

    event Withdrawal(uint256 value, address to, address from);

    function jail(address operator) external;

    function unJail(address operator) external;

    function freezeAndSlashingShares(
        address operator,
        uint256 slashShare
    ) external returns (uint256);

    function updateSlashingRecipient(address _slashingRecipient) external;

    function withdraw() external;

    function isOperatorJail(address operator) external view returns (bool);
}
