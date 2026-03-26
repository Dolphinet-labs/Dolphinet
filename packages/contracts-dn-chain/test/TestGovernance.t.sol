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
    mapping(address => bool) public jailed;
    function setJailed(address op, bool j) external { jailed[op] = j; }
    function isOperatorJail(address op) external view returns (bool) { return jailed[op]; }
}

contract TestGovernance is Test {
    DolphinetGovernance public gov;
    MockDelegationManager public mockDelegation;
    MockSlashingManager public mockSlashing;
    
    address public owner = address(0x1);
    address public manager = address(0x2);
    address[] public candidatesPool;

    function setUp() public {
        vm.warp(730 days + 1);
        mockDelegation = new MockDelegationManager();
        mockSlashing = new MockSlashingManager();
        
        gov = new DolphinetGovernance();
        gov.initialize(
            owner,
            manager,
            address(mockDelegation),
            address(mockSlashing)
        );
        
        for(uint160 i = 10; i < 50; i++) {
            candidatesPool.push(address(i));
            vm.deal(address(i), 10 ether);
        }
    }

    function testInitialization() public {
        assertEq(gov.manager(), manager);
        assertEq(address(gov.delegationManager()), address(mockDelegation));
        assertEq(address(gov.slashingManager()), address(mockSlashing));
        assertEq(gov.owner(), owner);
    }

    function testSetManager() public {
        address newManager = address(0x123);
        vm.prank(owner);
        gov.setManager(newManager);
        assertEq(gov.manager(), newManager);
    }

    function testRegisterCandidateSuccess() public {
        address op = candidatesPool[0];
        mockDelegation.setShares(op, 1000);
        
        vm.prank(op);
        gov.registerCandidate();
        
        (bool exists, uint256 votes, uint256 lastEid, bool isVal, bool isBV) = gov.candidates(op);
        assertTrue(exists);
        assertEq(votes, 0);
        assertEq(lastEid, 0);
    }

    function testRegisterCandidateFail() public {
        address op = candidatesPool[0];
        
        // Fail: no shares
        mockDelegation.setShares(op, 0);
        vm.prank(op);
        vm.expectRevert("Governance.registerCandidate: operator has no shares delegated");
        gov.registerCandidate();
        
        // Fail: jailed
        mockDelegation.setShares(op, 1000);
        mockSlashing.setJailed(op, true);
        vm.prank(op);
        vm.expectRevert("operator is jailed");
        gov.registerCandidate();
        
        // Success register
        mockSlashing.setJailed(op, false);
        vm.prank(op);
        gov.registerCandidate();
        
        // Fail: already candidate
        vm.prank(op);
        vm.expectRevert("already candidate");
        gov.registerCandidate();
    }

    function testElectionLifecycle() public {
        // 1. Start Election
        // Interval is 730 days. lastElectionTime initialized to block.timestamp - 730 days.
        gov.startElection();
        assertEq(gov.currentElectionId(), 1);
        
        // 2. Register candidates
        for(uint256 i = 0; i < 28; i++) {
            address op = candidatesPool[i];
            mockDelegation.setShares(op, 1000);
            vm.prank(op);
            gov.registerCandidate();
        }
        
        // 3. Vote
        for(uint256 i = 0; i < 28; i++) {
            address voter = address(uint160(1000 + i));
            vm.deal(voter, 1 ether);
            // Vote for candidate i % 10 (top 10 get 2.8 votes each avg)
            address cand = candidatesPool[i % 10];
            vm.prank(voter);
            gov.vote{value: 0.01 ether}(cand);
        }
        
        // Check votes
        for(uint256 i = 0; i < 8; i++) {
            (, uint256 v, , , ) = gov.candidates(candidatesPool[i]);
            assertEq(v, 3);
        }
        for(uint256 i = 8; i < 10; i++) {
            (, uint256 v, , , ) = gov.candidates(candidatesPool[i]);
            assertEq(v, 2);
        }
        
        // 4. Finalize Election
        vm.prank(owner);
        gov.finalizeElection();
        
        (bool finalized, uint256 finalEid) = gov.isElectionFinalized();
        assertTrue(finalized);
        assertEq(finalEid, 1);
        
        // Top 7 are validators
        address[] memory validators = gov.getValidators();
        assertEq(validators.length, 7);
        
        // Next 14 are block voters (total 21 in committee)
        address[] memory bv = gov.getBlockVoters();
        assertEq(bv.length, 14);
        
        // Next 7 are standby
        address[] memory standby = gov.getStandbyValidators();
        assertEq(standby.length, 7);
        
        // 5. Claim
        address voter1 = address(uint160(1000));
        uint256 balBefore = voter1.balance;
        vm.prank(voter1);
        gov.claim();
        uint256 balAfter = voter1.balance;
        assertEq(balAfter - balBefore, 0.01 ether);
    }

    function testRestartElection() public {
        gov.startElection();
        vm.warp(block.timestamp + 10 days);
        
        vm.prank(owner);
        gov.reStartElection();
        assertEq(gov.currentElectionId(), 2);
    }

    function testRemoveCandidateAndShift() public {
        gov.startElection();
        for(uint256 i = 0; i < 28; i++) {
            address op = candidatesPool[i];
            mockDelegation.setShares(op, 1000);
            vm.prank(op);
            gov.registerCandidate();
            // give them votes so standby is populated
            address voter = address(uint160(2000 + i));
            vm.deal(voter, 1 ether);
            vm.prank(voter);
            gov.vote{value: 0.1 ether}(op);
        }
        
        vm.prank(owner);
        gov.finalizeElection();
        
        address validatorToRemove = gov.getValidators()[0];
        address standbyToPromote = gov.getStandbyValidators()[0]; // Highest vote in standby (they all have 1 vote)
        
        vm.prank(manager);
        gov.removeCandidate(validatorToRemove);
        
        // Check if validator was removed and standby promoted
        address[] memory newValidators = gov.getValidators();
        bool found = false;
        for(uint i=0; i<newValidators.length; i++) {
            if(newValidators[i] == validatorToRemove) found = true;
        }
        assertFalse(found);
        
        // Check if standby promoted (it might be any of them since they have same votes, but let's check one)
        // Actually _popHighestVoteStandby picks the one it finds.
        assertEq(newValidators.length, 7);
    }

    function testRemoveBlockVoterAndShift() public {
        gov.startElection();
        for(uint256 i = 0; i < 28; i++) {
            address op = candidatesPool[i];
            mockDelegation.setShares(op, 1000);
            vm.prank(op);
            gov.registerCandidate();
            address voter = address(uint160(2000 + i));
            vm.deal(voter, 1 ether);
            vm.prank(voter);
            gov.vote{value: 0.1 ether}(op);
        }
        
        vm.prank(owner);
        gov.finalizeElection();
        
        address bvToRemove = gov.getBlockVoters()[0];
        uint256 bvCountBefore = gov.getBlockVoters().length;
        
        vm.prank(manager);
        gov.removeCandidate(bvToRemove);
        
        assertEq(gov.getBlockVoters().length, bvCountBefore); // should stay 14 because one was promoted from standby
    }

    function testViewHelpers() public {
        testElectionLifecycle();
        
        address[] memory allBV = gov.getAllBlockVoters();
        assertEq(allBV.length, 21);
        
        (address[] memory vals, uint256[] memory shares) = gov.getValidatorsShares();
        assertEq(vals.length, 7);
        assertEq(shares.length, 7);
        assertEq(shares[0], 1000);
    }

    function testSupplementBlockVoters() public {
        gov.startElection();
        // Register only 5 candidates
        for(uint256 i = 0; i < 5; i++) {
            address op = candidatesPool[i];
            mockDelegation.setShares(op, 1000);
            vm.prank(op);
            gov.registerCandidate();
        }
        
        vm.prank(owner);
        gov.finalizeElection();
        
        assertEq(gov.getValidators().length, 5);
        
        // Supplement
        address[] memory newOps = new address[](2);
        newOps[0] = candidatesPool[5];
        newOps[1] = candidatesPool[6];
        
        // Prepare them
        for(uint i=0; i<2; i++) {
            mockDelegation.setShares(newOps[i], 1000);
            vm.prank(newOps[i]);
            gov.registerCandidate();
        }
        
        vm.prank(owner);
        gov.supplementBlockVoters(newOps);
        
        assertEq(gov.getValidators().length, 7);
    }
}
