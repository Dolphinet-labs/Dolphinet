// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.24;

import "forge-std/Test.sol";
import "@/core/pos/ChainBase.sol";
import "@/core/pos/ChainDepositManager.sol";
import "@/core/pos/DelegationManager.sol";
import "@/core/pos/SlashingManager.sol";

import "@/access/PauserRegistry.sol";
import "@/interfaces/IDelegationManager.sol";
import "@/interfaces/ISlashingManager.sol";
import "@/interfaces/ISignatureUtils.sol";
import "@/interfaces/IGovernance.sol";

import "@openzeppelin/contracts/proxy/transparent/ProxyAdmin.sol";
import "@openzeppelin/contracts/proxy/transparent/TransparentUpgradeableProxy.sol";

contract TestSlashingManagerTest is Test {
    FdChainBase public fdChainBase;
    PauserRegistry public pauserregistry;
    FdChainDepositManager public fdChainDepositManager;
    DelegationManager public delegationManager;
    SlashingManager public slashingManager;
    IDelegationManager.QueuedWithdrawalParams[] public params;
    IDelegationManager.Withdrawal[] public withdraws;

    address public user1 = address(0x01);
    address public pauser1 = address(0x02);
    address public pauser2 = address(0x03);
    address[] public pausers;
    address public unpauser = address(0x04);

    address public owner = address(0x05);
    address public operator = address(0x06);
    address public operator1 = address(0x07);
    address public slasher = address(0x08);
    address public slashingReceipt = address(100);

    function setUp() public {
        pausers.push(pauser1);
        pausers.push(pauser2);
        pauserregistry = new PauserRegistry(pausers, unpauser);

        FdChainBase logic1 = new FdChainBase();
        TransparentUpgradeableProxy proxy1 = new TransparentUpgradeableProxy(
            address(logic1),
            owner,
            ""
        );

        FdChainDepositManager logic2 = new FdChainDepositManager();
        TransparentUpgradeableProxy proxy2 = new TransparentUpgradeableProxy(
            address(logic2),
            owner,
            ""
        );

        DelegationManager logic3 = new DelegationManager();
        TransparentUpgradeableProxy proxy3 = new TransparentUpgradeableProxy(
            address(logic3),
            owner,
            ""
        );

        SlashingManager logic4 = new SlashingManager();
        TransparentUpgradeableProxy proxy4 = new TransparentUpgradeableProxy(
            address(logic4),
            owner,
            ""
        );

        fdChainBase = FdChainBase(payable(address(proxy1)));
        fdChainDepositManager = FdChainDepositManager(payable(address(proxy2)));
        delegationManager = DelegationManager(payable(address(proxy3)));
        slashingManager = SlashingManager(payable(address(proxy4)));

        fdChainBase.initialize(
            IPauserRegistry(address(pauserregistry)),
            1 ether,
            10 ether,
            IFdChainDepositManager(address(fdChainDepositManager))
        );
        fdChainDepositManager.initialize(
            owner,
            IDelegationManager(address(delegationManager)),
            fdChainBase
        );
        delegationManager.initialize(
            owner,
            IPauserRegistry(address(pauserregistry)),
            0,
            0,
            fdChainDepositManager,
            fdChainBase,
            slashingManager,
            IGovernance(address(0x09))
        );
        slashingManager.initialize(
            owner,
            IDelegationManager(address(delegationManager)),
            slasher,
            0.1 ether,
            slashingReceipt
        );

        vm.deal(user1, 100 ether);
        vm.deal(operator, 100 ether);
        vm.deal(operator1, 100 ether);
        vm.deal(slasher, 100 ether);
    }

    function testJailAndUnjailOperator() public {
        vm.prank(slasher);
        slashingManager.jail(operator);
        assertTrue(slashingManager.isOperatorJail(operator));

        vm.prank(slasher);
        slashingManager.unJail(operator);
        assertFalse(slashingManager.isOperatorJail(operator));

        vm.prank(user1);
        vm.expectRevert(
            "SlashingManager.onlySlasher: only slasher can do this operation"
        );
        slashingManager.jail(operator);

        vm.prank(user1);
        vm.expectRevert(
            "SlashingManager.onlySlasher: only slasher can do this operation"
        );
        slashingManager.unJail(operator);
    }

    function _registerAndDelegate() internal {
        DelegationManager.OperatorDetails memory od = IDelegationManager
            .OperatorDetails({
                earningsReceiver: operator,
                delegationApprover: address(0),
                stakerOptOutWindowBlocks: 100
            });
        ISignatureUtils.SignatureWithExpiry memory emptySignatureAndExpiry;

        vm.prank(operator);
        fdChainDepositManager.depositIntoDolphinnetChain{value: 1 ether}(1 ether);
        vm.prank(operator);
        delegationManager.registerAsOperator(od, "node-url");
        vm.prank(operator1);
        fdChainDepositManager.depositIntoDolphinnetChain{value: 1 ether}(1 ether);
        vm.prank(operator1);
        delegationManager.registerAsOperator(od, "node-url");

        vm.prank(user1);
        fdChainDepositManager.depositIntoDolphinnetChain{value: 2 ether}(2 ether);

        vm.prank(user1);
        delegationManager.delegateTo(
            operator,
            emptySignatureAndExpiry,
            bytes32(0)
        );

        assertEq(
            delegationManager.stakerDelegateSharesToOperator(operator, user1),
            2 ether
        );
    }

    function testFreezeAndSlashingSharesSuccess() public {
        vm.prank(slasher);
        vm.expectRevert(
            "SlashingManager.freezeOperatorStakingShares: No shares to distribute slashShare"
        );
        uint256 returned = slashingManager.freezeAndSlashingShares(
            operator,
            1 ether
        );

        _registerAndDelegate();

        vm.prank(user1);
        vm.expectRevert(
            "SlashingManager.onlySlasher: only slasher can do this operation"
        );
        returned = slashingManager.freezeAndSlashingShares(operator, 1 ether);

        vm.prank(slasher);
        returned = slashingManager.freezeAndSlashingShares(operator, 1 ether);

        assertEq(returned, 1 ether);
        assertEq(fdChainDepositManager.getDeposits(user1), 1333333333333333334);
        assertEq(address(slashingManager).balance, 999999999999999999);
    }

    function testUpdateSlashingRecipient() public {
        address newRecipient = address(0xCAFE);

        vm.prank(user1);
        vm.expectRevert(
            "SlashingManager.onlySlasher: only slasher can do this operation"
        );
        slashingManager.updateSlashingRecipient(newRecipient);

        vm.prank(slasher);
        slashingManager.updateSlashingRecipient(newRecipient);
        assertEq(slashingManager.slashingRecipient(), newRecipient);
    }

    function testWithdrawRevertsBelowThreshold() public {
        vm.deal(address(slashingManager), 0.05 ether);
        vm.expectRevert(
            "SlashingManager: withdrawal amount must be greater than minimum withdrawal amount"
        );
        slashingManager.withdraw();

        vm.deal(address(slashingManager), 0.5 ether);
        vm.prank(slasher);
        slashingManager.updateSlashingRecipient(address(0x09));
        vm.expectRevert("FeeVault: SlashingManager to send ETH to recipient");
        slashingManager.withdraw();

        vm.prank(slasher);
        slashingManager.updateSlashingRecipient(slashingReceipt);

        vm.deal(address(slashingManager), 0.5 ether);
        slashingManager.withdraw();
        assertEq(slashingReceipt.balance, 0.5 ether);
    }

    function testWithdrawSuccess() public {
        vm.prank(slasher);
        slashingManager.updateSlashingRecipient(slashingReceipt);

        vm.deal(address(slashingManager), 2 ether);
        vm.expectEmit(true, true, true, true);
        emit ISlashingManager.Withdrawal(
            2 ether,
            slashingReceipt,
            address(this)
        );

        slashingManager.withdraw();
        assertEq(slashingReceipt.balance, 2 ether);
    }
}
