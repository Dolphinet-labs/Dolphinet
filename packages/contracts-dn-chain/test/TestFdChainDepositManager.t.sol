// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.24;

import "forge-std/Test.sol";
import "@/interfaces/IGovernance.sol";
import "@/core/pos/ChainBase.sol";
import "@/core/pos/ChainDepositManager.sol";
import "@/core/pos/DelegationManager.sol";

import "@/access/PauserRegistry.sol";
import "@/interfaces/IDelegationManager.sol";
import "@/interfaces/ISlashingManager.sol";

import "@openzeppelin/contracts/proxy/transparent/ProxyAdmin.sol";
import "@openzeppelin/contracts/proxy/transparent/TransparentUpgradeableProxy.sol";

contract CPChainDepositManagerTest is Test {
    FdChainBase public fdChainBase;
    PauserRegistry public pauserregistry;
    FdChainDepositManager public fdChainDepositManager;
    DelegationManager public delegationManager;

    address public user1 = address(0x01);
    address public pauser1 = address(0x02);
    address public pauser2 = address(0x03);
    address[] public pausers;
    address public unpauser = address(0x04);

    address public owner = address(0x05);
    address public slashingManager = address(0x06);

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

        fdChainBase = FdChainBase(payable(address(proxy1)));
        fdChainDepositManager = FdChainDepositManager(payable(address(proxy2)));
        delegationManager = DelegationManager(payable(address(proxy3)));

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
            ISlashingManager(address(slashingManager)),
            IGovernance(address(0x09))
        );

        vm.deal(user1, 100 ether);
    }

    function testDepositIntoDolphinnetChain() public {
        uint256 beforeShares = fdChainDepositManager.getDeposits(user1);

        vm.startPrank(user1);
        fdChainDepositManager.depositIntoDolphinnetChain{value: 2 ether}(
            2 ether
        );
        uint256 afterShares = fdChainDepositManager.getDeposits(user1);
        uint256 shares = fdChainBase.shares(user1);
        uint256 value1 = fdChainBase.userUnderlying(user1);
        uint256 value2 = fdChainBase.userUnderlyingView(user1);

        assertGt(afterShares, beforeShares);
        assert(afterShares == 2 ether);
        assert(shares == 2 ether);
        assert(value1 == 2 ether);
        assert(value2 == 2 ether);
        assert(fdChainDepositManager.stakerFdChainBaseShares(user1) == 2 ether);
        vm.stopPrank();
    }

    function testRemoveSharesOnlyDelegationManager() public {
        vm.prank(user1);
        fdChainDepositManager.depositIntoDolphinnetChain{value: 2 ether}(
            2 ether
        );

        vm.prank(address(delegationManager));
        fdChainDepositManager.removeShares(user1, 1 ether);
        assertEq(fdChainDepositManager.getDeposits(user1), 1 ether);

        vm.expectRevert("onlyDelegationManager");
        fdChainDepositManager.removeShares(user1, 1 ether);

        vm.expectRevert(
            "FdChainDepositManager.removeShares: shareAmount should not be zero!"
        );
        vm.prank(address(delegationManager));
        fdChainDepositManager.removeShares(user1, 0);

        vm.expectRevert(
            "FdChainDepositManager._removeShares: shareAmount too high"
        );
        vm.prank(address(delegationManager));
        fdChainDepositManager.removeShares(user1, 100 ether);
    }

    function testWithdrawSharesAsDolOnlyDelegationManager() public {
        vm.startPrank(user1);
        fdChainDepositManager.depositIntoDolphinnetChain{value: 2 ether}(
            2 ether
        );
        vm.stopPrank();

        vm.prank(address(delegationManager));
        fdChainDepositManager.withdrawSharesAsDol(user1, 1 ether);

        assertEq(user1.balance, 99 ether); // 或 assertGt(user.balance, 0);

        vm.expectRevert("onlyDelegationManager");
        fdChainDepositManager.withdrawSharesAsDol(user1, 1 ether);
    }

    function testAddShares() public {
        vm.prank(address(delegationManager));
        fdChainDepositManager.addShares(user1, 5 ether);

        uint256 stored = fdChainDepositManager.getDeposits(user1);
        assertEq(stored, 5 ether);

        vm.prank(address(delegationManager));
        vm.expectRevert(
            "FdChainDepositManager._addShares: shares should not be zero!"
        );
        fdChainDepositManager.addShares(user1, 0);

        vm.expectRevert("onlyDelegationManager");
        fdChainDepositManager.addShares(user1, 1 ether);
    }

    function testSignatureWithVmSign() public pure {
        uint256 privKey = 0x4f3edf983ac636a65a842ce7c78d9aa706d3b113b37c9430e6fd8c8d11f8b4e6;
        address expectedSigner = vm.addr(privKey);

        // 构造 EIP-712 的 digestHash（你可以通过 helper 函数生成）
        bytes32 digest = 0x349dbac25db23f8be0b94a49883a681b0deebbb21ec6eb1c4a2676c9d5e1e222;

        (uint8 v, bytes32 r, bytes32 s) = vm.sign(privKey, digest);

        bytes memory signature = abi.encodePacked(r, s, v);

        address result = ECDSA.recover(digest, signature);
        assertEq(result, expectedSigner);
    }

    function testdepositIntoDolphinnetChainWithSignature() public {
        uint256 privKey = 0x4f3edf983ac636a65a842ce7c78d9aa706d3b113b37c9430e6fd8c8d11f8b4e6;
        address staker = vm.addr(privKey);
        vm.deal(staker, 10 ether);

        bytes32 DEPOSIT_TYPEHASH = keccak256(
            "Deposit(address staker,address FdChainBase,uint256 amount,uint256 nonce,uint256 expiry)"
        );
        bytes32 DOMAIN_TYPEHASH = keccak256(
            "EIP712Domain(string name,uint256 chainId,address verifyingContract)"
        );

        bytes32 structHash = keccak256(
            abi.encode(DEPOSIT_TYPEHASH, staker, 2 ether, 0, 100)
        );

        bytes32 domainSeparator = keccak256(
            abi.encode(
                DOMAIN_TYPEHASH,
                keccak256("dolphinnetChain"),
                block.chainid,
                address(fdChainDepositManager)
            )
        );
        bytes32 digestHash = keccak256(
            abi.encodePacked("\x19\x01", domainSeparator, structHash)
        );
        bytes32 digest = keccak256(
            abi.encodePacked("\x19Ethereum Signed Message:\n32", digestHash)
        );

        (uint8 v, bytes32 r, bytes32 s) = vm.sign(privKey, digest);
        bytes memory signature = abi.encodePacked(r, s, v);

        fdChainDepositManager.depositIntoDolphinnetChainWithSignature{
            value: 2 ether
        }(2 ether, staker, 100, signature);

        uint256 afterShares = fdChainDepositManager.getDeposits(staker);
        assert(afterShares == 2 ether);

        vm.expectRevert("deposit value not match amount");
        fdChainDepositManager.depositIntoDolphinnetChainWithSignature{
            value: 2 ether
        }(10 ether, staker, 100, signature);

        vm.warp(10000);
        vm.expectRevert(
            "FdChainDepositManager.depositIntoDolphinnetChainWithSignature: signature expired"
        );
        fdChainDepositManager.depositIntoDolphinnetChainWithSignature{
            value: 2 ether
        }(2 ether, staker, 100, signature);
    }
}
