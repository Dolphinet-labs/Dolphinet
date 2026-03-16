// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "../../interfaces/IDelegationManager.sol";
import "../../interfaces/ISlashingManager.sol";

contract GovernanceStorage {
    // ===== Core Parameters =====

    uint256 public constant MAX_BLOCK_VOTERS = 21; // Committee size
    uint256 public constant NUM_VALIDATORS = 7; // Number of block producers

    uint256 public constant ELECTION_INTERVAL = 730 days;
    uint256 public constant MIN_VOTER_BALANCE = 0.001 ether;

    uint256 internal constant NUM_STANDBY = 7;
    uint256 internal constant MAX_RANKED = 28; // 7 + 14 + 7

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

    // ===== NEW: Standby validators (rank 22-28) =====
    address[] internal standbyValidators;

    // ===== NEW: Election lifecycle flag =====
    bool internal electionFinalized;
    uint256 internal finalizedElectionId;

    // ===== NEW: Fast role lookup to support shifting on removal =====
    enum RankRole {
        NONE,
        VALIDATOR,
        BLOCK_VOTER,
        STANDBY
    }
    mapping(address => RankRole) internal roleOf;
    mapping(address => uint256) internal indexOf; // index in its array (validators/blockVoters/standbyValidators)

    mapping(address => uint256) public voterLockedBalance;

    ISlashingManager public slashingManager;

    uint256[45] private __gap;
}
