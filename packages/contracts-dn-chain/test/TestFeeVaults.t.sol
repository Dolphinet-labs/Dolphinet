// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "forge-std/Test.sol";
import "@/core/fee/SequencerFeeVault.sol";

contract TestFeeVaults is Test {
    SequencerFeeVault public vault;
    address public recipient = address(0x55);
    uint256 public minWithdrawal = 1 ether;

    function setUp() public {
        vault = new SequencerFeeVault(recipient, minWithdrawal);
    }

    function testBasicInfo() public {
        assertEq(vault.RECIPIENT(), recipient);
        assertEq(vault.recipient(), recipient);
        assertEq(vault.feeWallet(), recipient);
        assertEq(vault.MIN_WITHDRAWAL_AMOUNT(), minWithdrawal);
        assertEq(vault.minWithdrawalAmount(), minWithdrawal);
        assertEq(vault.version(), "0.0.1");
    }

    function testReceive() public {
        uint256 amount = 0.5 ether;
        (bool success, ) = address(vault).call{value: amount}("");
        assertTrue(success);
        assertEq(address(vault).balance, amount);
    }

    function testWithdrawSuccess() public {
        uint256 amount = 1.5 ether;
        vm.deal(address(vault), amount);
        
        uint256 balBefore = recipient.balance;
        vault.withdraw();
        uint256 balAfter = recipient.balance;
        
        assertEq(balAfter - balBefore, amount);
        assertEq(address(vault).balance, 0);
        assertEq(vault.totalProcessed(), amount);
    }

    function testWithdrawFailMinAmount() public {
        uint256 amount = 0.5 ether;
        vm.deal(address(vault), amount);
        
        vm.expectRevert("FeeVault: withdrawal amount must be greater than minimum withdrawal amount");
        vault.withdraw();
    }

    function testWithdrawTransferFail() public {
        // Create a recipient that reverts on receiving ETH
        RevertingRecipient revRecipient = new RevertingRecipient();
        SequencerFeeVault vault2 = new SequencerFeeVault(address(revRecipient), 1 ether);
        
        vm.deal(address(vault2), 2 ether);
        vm.expectRevert("FeeVault: failed to send TW to fee recipient");
        vault2.withdraw();
    }
}

contract RevertingRecipient {
    receive() external payable {
        revert("I refuse money");
    }
}
