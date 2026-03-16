// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.24;

import "forge-std/Test.sol";
import "@/core/pos/ChainBase.sol";
import "@/access/PauserRegistry.sol";

import "@openzeppelin/contracts/proxy/transparent/ProxyAdmin.sol";
import "@openzeppelin/contracts/proxy/transparent/TransparentUpgradeableProxy.sol";

contract FdChainBaseTest is Test {
    FdChainBase public fdChainBase;
    PauserRegistry public pauserregistry;

    address public user1 = address(0x01);
    address public pauser1 = address(0x02);
    address public pauser2 = address(0x03);
    address[] public pausers;
    address public unpauser = address(0x04);
    address public owner = address(0x05);
    address public strategyManager = address(0x06);

    function setUp() public {
        vm.deal(strategyManager, 10 ether);
        pausers.push(pauser1);
        pausers.push(pauser2);
        pauserregistry = new PauserRegistry(pausers, unpauser);

        FdChainBase logic = new FdChainBase();
        TransparentUpgradeableProxy proxy = new TransparentUpgradeableProxy(
            address(logic),
            owner,
            ""
        );

        fdChainBase = FdChainBase(payable(address(proxy)));

        fdChainBase.initialize(
            IPauserRegistry(address(pauserregistry)),
            1 ether,
            10 ether,
            IFdChainDepositManager(address(strategyManager))
        );
    }

    function testDepositShouldMintShares() public {
        uint256 balanceBefore = address(fdChainBase).balance;

        vm.deal(strategyManager, 10 ether);
        vm.prank(strategyManager);

        uint256 newShares = fdChainBase.deposit{value: 10 ether}(
            10 ether,
            user1
        );

        uint256 balanceAfter = address(fdChainBase).balance;

        assertGt(newShares, 0);
        assert(balanceAfter > balanceBefore);
        assertEq(fdChainBase.totalShares(), newShares);
    }

    function testDepositTooLowOrHighShouldRevert() public {
        vm.expectRevert(
            "FdChainBase: deposit token must more than min deposit amount"
        );
        vm.deal(strategyManager, 10 ether);
        vm.prank(strategyManager);

        fdChainBase.deposit{value: 0.5 ether}(0.5 ether, user1);


        vm.expectRevert(
            "FdChainBase: deposit token must less than max deposit amount"
        );
        vm.deal(strategyManager, 15 ether);
        vm.prank(strategyManager);

        fdChainBase.deposit{value: 12 ether}(12 ether, user1);

    }

    function testWithdrawShouldSendEth() public {
        vm.prank(strategyManager);

        uint256 newShares = fdChainBase.deposit{value: 5 ether}(5 ether, user1);

        uint256 balanceBefore = address(fdChainBase).balance;

        vm.prank(user1);
        vm.expectRevert("FdChainBase.onlyStrategyManager");
        fdChainBase.withdraw(user1, newShares);

        vm.prank(strategyManager);
        fdChainBase.withdraw(user1, newShares);
        uint256 balanceAfter = address(fdChainBase).balance;

        assertGt(balanceBefore, balanceAfter);
        assert(user1.balance == 5 ether);
    }

    function testWithdrawTooMuchShouldRevert() public {
        vm.expectRevert(
            "FdChainBase.withdraw: amountShares must be less than or equal to totalShares"
        );
        vm.prank(strategyManager);
        fdChainBase.withdraw(address(this), 100 ether);
    }

    function testSetDepositLimits() public {
        vm.prank(strategyManager);
        fdChainBase.setDepositLimits(2 ether, 200 ether);
        (uint256 minD, uint256 maxD) = fdChainBase.getDepositLimits();
        assertEq(minD, 2 ether);
        assertEq(maxD, 200 ether);
    }

    function testSharesToUnderlyingMutableView() public view {
        uint256 underlying = fdChainBase.sharesToUnderlying(5 ether);
        assertEq(underlying, 5 ether);

        uint256 underlying1 = fdChainBase.sharesToUnderlyingView(5 ether);
        assertEq(underlying1, 5 ether);

        uint256 share = fdChainBase.underlyingToSharesView(1 ether);
        assertEq(share, 1 ether);

        uint256 share1 = fdChainBase.underlyingToShares(1 ether);
        assertEq(share1, 1 ether);
    }

    function testExplanation() public view {
        string memory expectResult = "Dolphinnet Chain Pos Staking Protocol";
        string memory result = fdChainBase.explanation();
        assertEq(result, expectResult);
    }

    function testPaused() public {
        vm.prank(pauser1);
        fdChainBase.pauseAll();

        vm.prank(strategyManager);
        vm.expectRevert("Pausable: contract is paused");
        fdChainBase.withdraw(user1, 1 ether);
    }
}
