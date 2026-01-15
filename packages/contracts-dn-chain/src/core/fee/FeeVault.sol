// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {SafeCall} from "src/libraries/SafeCall.sol";
import {Predeploys} from "src/libraries/Predeploys.sol";
import {Types} from "src/libraries/Types.sol";

abstract contract FeeVault {
    uint256 public immutable MIN_WITHDRAWAL_AMOUNT;

    address public immutable RECIPIENT;

    uint32 internal constant WITHDRAWAL_MIN_GAS = 400_000;

    uint256 public totalProcessed;

    uint256[48] private __gap;

    event Withdrawal(uint256 value, address to, address from);

    constructor(address _recipient, uint256 _minWithdrawalAmount) {
        RECIPIENT = _recipient;
        MIN_WITHDRAWAL_AMOUNT = _minWithdrawalAmount;
    }

    receive() external payable {}

    function minWithdrawalAmount() public view returns (uint256 amount_) {
        amount_ = MIN_WITHDRAWAL_AMOUNT;
    }

    function recipient() public view returns (address recipient_) {
        recipient_ = RECIPIENT;
    }

    function withdraw() external {
        require(
            address(this).balance >= MIN_WITHDRAWAL_AMOUNT,
            "FeeVault: withdrawal amount must be greater than minimum withdrawal amount"
        );

        uint256 value = address(this).balance;
        totalProcessed += value;

        emit Withdrawal(value, RECIPIENT, msg.sender);

        (bool success,) = payable(RECIPIENT).call{value: value}("");

        require(success, "FeeVault: failed to send DN to fee recipient");
    }
}
