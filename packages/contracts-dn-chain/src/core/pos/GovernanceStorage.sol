// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "../../interfaces/IDelegationManager.sol";

contract GovernanceStorage {
    // ===== Core Parameters =====

    uint256 public constant MAX_BLOCK_VOTERS = 21; // Committee size
    uint256 public constant NUM_VALIDATORS = 7; // Number of block producers

    uint256 public constant ELECTION_INTERVAL = 730 days;
    uint256 public constant MIN_VOTER_BALANCE = 0.001 ether;

    address public constant BURN_ADDRESS =
        address(0x0000dEaD000000000000000000000000000000dEaD);

    address public manager;

    modifier onlyManager() {
        require(msg.sender == manager, "not manager");
        _;
    }

    // ===== Candidate Structure =====
    struct Candidate {
        bool exists;
        uint256 votes; // Votes in current election round
        uint256 lastElectionId; // Last participated election round
        bool isValidator; // Is currently in top 7
        bool isBlockVoter; // Is currently in top 21
    }

    mapping(address => Candidate) public candidates;
    address[] public candidateList;

    // Current active validator + committee sets
    address[] public validators;
    address[] public blockVoters;

    // ===== Election State =====
    uint256 public currentElectionId;
    uint256 public lastElectionTime;

    // Prevent double voting
    mapping(uint256 => mapping(address => bool)) public hasVoted;

    IDelegationManager public delegationManager;

    uint256[45] private __gap;
}
