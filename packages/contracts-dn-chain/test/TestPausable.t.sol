// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.24;

import "forge-std/Test.sol";
import "@/access/Pausable.sol";
import "@/access/PauserRegistry.sol";

contract PausableMock is Pausable {
    function initialize(IPauserRegistry _pauserRegistry, uint256 initPausedStatus) external {
        _initializePauser(_pauserRegistry, initPausedStatus);
    }
}

contract TestPausable is Test {
    PausableMock public pausable;
    PauserRegistry public registry;
    address public pauser = address(0x11);
    address public unpauser = address(0x22);
    address public other = address(0x33);

    function setUp() public {
        address[] memory pausers = new address[](1);
        pausers[0] = pauser;
        registry = new PauserRegistry(pausers, unpauser);
        pausable = new PausableMock();
        pausable.initialize(registry, 0);
    }

    function testInitialization() public {
        assertEq(address(pausable.pauserRegistry()), address(registry));
        assertEq(pausable.paused(), 0);
    }

    function testInitializeFail() public {
        vm.expectRevert("Pausable._initializePauser: _initializePauser() can only be called once");
        pausable.initialize(registry, 0);
    }

    function testPauseSuccess() public {
        vm.prank(pauser);
        pausable.pause(1);
        assertEq(pausable.paused(), 1);
        assertTrue(pausable.paused(0));
        assertFalse(pausable.paused(1));
    }

    function testPauseManyBits() public {
        vm.prank(pauser);
        pausable.pause(7); // bits 0, 1, 2
        assertEq(pausable.paused(), 7);
        assertTrue(pausable.paused(0));
        assertTrue(pausable.paused(1));
        assertTrue(pausable.paused(2));
        assertFalse(pausable.paused(3));
    }

    function testPauseFailNonPauser() public {
        vm.prank(other);
        vm.expectRevert("msg.sender is not permissioned as pauser");
        pausable.pause(1);
    }

    function testPauseFailUnpauseAttempt() public {
        vm.prank(pauser);
        pausable.pause(3); // pause 0 and 1
        
        vm.prank(pauser);
        vm.expectRevert("Pausable.pause: invalid attempt to unpause functionality");
        pausable.pause(1); // trying to set to 1, which unpauses bit 1
    }

    function testPauseAll() public {
        vm.prank(pauser);
        pausable.pauseAll();
        assertEq(pausable.paused(), type(uint256).max);
        assertTrue(pausable.paused(0));
        assertTrue(pausable.paused(255));
    }

    function testUnpauseAll() public {
        vm.prank(pauser);
        pausable.pauseAll();
        
        vm.prank(unpauser);
        pausable.unpauseAll();
        assertEq(pausable.paused(), 0);
    }

    function testUnpauseSuccess() public {
        vm.prank(pauser);
        pausable.pause(3);
        
        vm.prank(unpauser);
        pausable.unpause(1); // unpause bit 1, status becomes 1
        assertEq(pausable.paused(), 1);
    }

    function testUnpauseFailNonUnpauser() public {
        vm.prank(pauser);
        pausable.pause(1);
        
        vm.prank(other);
        vm.expectRevert("msg.sender is not permissioned as unpauser");
        pausable.unpause(0);
    }

    function testUnpauseFailPauseAttempt() public {
        vm.prank(pauser);
        pausable.pause(1);
        
        vm.prank(unpauser);
        vm.expectRevert("Pausable.unpause: invalid attempt to pause functionality");
        pausable.unpause(3); // trying to set to 3, which pauses bit 1
    }

    function testSetPauserRegistry() public {
        PauserRegistry newRegistry = new PauserRegistry(new address[](0), unpauser);
        vm.prank(unpauser);
        pausable.setPauserRegistry(newRegistry);
        assertEq(address(pausable.pauserRegistry()), address(newRegistry));
    }

    function testSetPauserRegistryFail() public {
        vm.prank(pauser);
        vm.expectRevert("msg.sender is not permissioned as unpauser");
        pausable.setPauserRegistry(registry);
        
        vm.prank(unpauser);
        vm.expectRevert("Pausable._setPauserRegistry: newPauserRegistry cannot be the zero address");
        pausable.setPauserRegistry(IPauserRegistry(address(0)));
    }
}
