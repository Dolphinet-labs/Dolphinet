// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/// @title  Governance Interface
/// @notice Interface for validator governance, election, staking,
///         committee formation, and manager slashing controls.
/// @dev DOL is native coin, not ERC20.
interface IGovernance {
    // ========= EVENTS =========

    event ManagerChanged(
        address indexed oldManager,
        address indexed newManager
    );

    event CandidateRegistered(address indexed operator, uint256 stake);
    event CandidateStakeIncreased(address indexed operator, uint256 newStake);
    event CandidateStakeWithdrawn(
        address indexed operator,
        uint256 remainingStake,
        uint256 amount
    );
    event CandidateRemoved(address indexed operator);

    event ElectionStarted(uint256 indexed electionId, uint256 timestamp);
    event Voted(
        uint256 indexed electionId,
        address indexed voter,
        address indexed candidate
    );

    event ElectionFinalized(
        uint256 indexed electionId,
        address[] validators,
        address[] blockVoters
    );

    event Slashed(
        address indexed validator,
        uint256 indexed electionId,
        uint256 penalty,
        uint256 remainingStake
    );

    event StandbyValidatorsSelected(
        uint256 indexed electionId,
        address[] standby
    );
    event SupplementBlockVoters(uint256 indexed electionId, address[] added);
    event ForceUnregisterFailed(
        uint256 indexed electionId,
        address indexed operator
    );
    event ElectionFinalizedFlag(uint256 indexed electionId);

    // ========= ADMIN =========

    /// @notice Set governance manager address
    function setManager(address _manager) external;

    // ========= CANDIDATE / STAKING =========

    /// @notice Register as validator candidate (stake native DOL)
    function registerCandidate() external;

    /// @notice View candidate list
    function getCandidates() external view returns (address[] memory);

    // ========= ELECTION =========

    /// @notice Start a new election (can run only every interval period)
    function startElection() external;

    /// @notice Finalize election
    /// @dev Select Top 21 Block Voters + Top 7 Validators
    function finalizeElection() external;

    // ========= COMMITTEE VIEW =========

    /// @notice Get current validator set (7 max)
    function getValidators() external view returns (address[] memory);

    /// @notice Get Block Voters Committee (Top 21)
    function getBlockVoters() external view returns (address[] memory);

    // ========= SLASHING (MANAGER ONLY) =========

    /// @notice Slash validator / committee member
    /// @param validator address to slash
    /// @param permille penalty in ‰ (permille)
    function slash(address validator, uint256 permille) external;

    function removeCandidate(address op) external;
}
