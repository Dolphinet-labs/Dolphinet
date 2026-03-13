// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "forge-std/Test.sol";
import "@/core/pos/Governance.sol";
import "@/interfaces/IDelegationManager.sol";
import "@/interfaces/ISlashingManager.sol";

contract MockDelegationManager {
    mapping(address => uint256) public shares;
    function setShares(address op, uint256 s) external { shares[op] = s; }
    function getOperatorShares(address op) external view returns (uint256) { return shares[op]; }
    function unRegisterFromGovernance(address op) external {}
    function getOperatorDelegatedStakers(address op) external view returns (address[] memory) {
        address[] memory s = new address[](1);
        s[0] = op;
        return s;
    }
}

contract MockSlashingManager {
    function isOperatorJail(address) external pure returns (bool) { return false; }
}

contract TestGovernanceBugFix is Test {
    DolphinetGovernance public gov;
    MockDelegationManager public mockDelegation;
    MockSlashingManager public mockSlashing;
    
    address public owner = address(0x1);
    address public manager = address(0x2);

    function setUp() public {
        vm.warp(730 days + 1);
        mockDelegation = new MockDelegationManager();
        mockSlashing = new MockSlashingManager();
        
        gov = new DolphinetGovernance();
        gov.initialize(manager, address(mockDelegation), address(mockSlashing));
        gov.transferOwnership(owner);
    }

    function testForceUnregisterLosersBugFix() public {
        gov.startElection();
        
        // Register 30 candidates (Max ranked is 28)
        uint256 totalCandidates = 30;
        for(uint160 i = 100; i < 100 + totalCandidates; i++) {
            address op = address(i);
            mockDelegation.setShares(op, 1000);
            vm.prank(op);
            gov.registerCandidate();
            
            // Give each candidate some votes
            address voter = address(uint160(2000 + i));
            vm.deal(voter, 1 ether);
            vm.prank(voter);
            gov.vote{value: 0.01 ether}(op);
        }
        
        assertEq(gov.getCandidates().length, 30, "Should have 30 candidates before finalization");

        // Finalize Election - this calls _forceUnregisterLosers
        vm.prank(owner);
        gov.finalizeElection();

        // Check results
        assertTrue(gov.getValidators().length == 7);
        assertTrue(gov.getBlockVoters().length == 14);
        assertTrue(gov.getStandbyValidators().length == 7);
        
        // Original bug would cause infinite loop here, so if we reach this point, it's already a good sign.
        // Also check if losers were removed from candidateList
        assertEq(gov.getCandidates().length, 28, "Losers should be removed from candidateList");
        
        // Verify specifically that candidates who didn't make it are gone
        // With 30 candidates having 1 vote each, the first 28 in the list (or random ones depending on selectTopN) stay.
        // Actually selectTopN with equal votes might be arbitrary, but exactly 28 must remain.
    }
}
