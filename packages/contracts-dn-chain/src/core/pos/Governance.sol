// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin-upgrades/contracts/proxy/utils/Initializable.sol";
import "@openzeppelin-upgrades/contracts/access/OwnableUpgradeable.sol";
import "./GovernanceStorage.sol";
import "../../interfaces/IGovernance.sol";

/**
 * Dolphinet Governance Contract
 *
 * Responsibilities:
 *  - Allow node operators to register as validator candidates by staking native DOL
 *  - Allow eligible DOL holders to vote for candidates every election cycle
 *  - Select:
 *      1) Top 21 "Block Voters Committee"
 *      2) Top 7 "Validators" from those 21
 *  - Support stake increase / partial withdrawal
 *  - Remove candidate if stake drops below minimum threshold
 *  - Provide Committee + Validator sets to Manager
 *  - Allow Manager to slash validators/block voters through penalties
 *
 * Key Cycles / Parameters:
 *  - Election runs once every 2 years (730 days)
 *  - Candidates must stake native DOL:
 *      min = 320,000 DOL
 *      max = 1,820,000 DOL
 *  - Voters must hold a minimum DOL balance
 *  - Vote power = 1 per address (can be enhanced later)
 */

abstract contract DolphinetGovernance is
    Initializable,
    OwnableUpgradeable,
    GovernanceStorage,
    IGovernance
{
    function initialize(
        address _manager,
        address _delegationManager
    ) public initializer {
        __Ownable_init(msg.sender);
        manager = _manager;
        delegationManager = IDelegationManager(_delegationManager);
        lastElectionTime = block.timestamp - ELECTION_INTERVAL; // Allow immediate election start
    }

    // ================= ADMIN =================

    function setManager(address _manager) external onlyOwner {
        emit ManagerChanged(manager, _manager);
        manager = _manager;
    }

    // ================= CANDIDATE LOGIC =================

    /**
     * Register as validator candidate.
     * Requires staking native DOL within allowed range.
     */
    function registerCandidate() external {
        require(!candidates[msg.sender].exists, "already candidate");
        uint256 stakedAmount;

        // requireing candidate to have delegated shares(as operator)
        uint256 shares = delegationManager.getOperatorShares(msg.sender);
        require(
            shares > 0,
            "Governance.registerCandidate: operator has no shares delegated"
        );

        Candidate storage c = candidates[msg.sender];
        c.exists = true;
        c.votes = 0;
        c.lastElectionId = currentElectionId;
        c.isValidator = false;
        c.isBlockVoter = false;

        candidateList.push(msg.sender);

        emit CandidateRegistered(msg.sender, stakedAmount);
    }

    function removeCandidate(address op) external onlyManager {
        _removeCandidate(op);
    }

    /**
     * Internal helper to remove a candidate completely.
     * Removes:
     *  - Candidate record
     *  - Committee membership
     *  - Validator membership
     */
    function _removeCandidate(address op) internal {
        Candidate storage c = candidates[op];
        if (!c.exists) return;

        c.exists = false;
        c.isValidator = false;
        c.isBlockVoter = false;
        c.votes = 0;

        // Remove from candidate list
        uint256 len = candidateList.length;
        for (uint256 i = 0; i < len; i++) {
            if (candidateList[i] == op) {
                candidateList[i] = candidateList[len - 1];
                candidateList.pop();
                break;
            }
        }

        // Remove from validators
        uint256 vlen = validators.length;
        for (uint256 j = 0; j < vlen; j++) {
            if (validators[j] == op) {
                validators[j] = validators[vlen - 1];
                validators.pop();
                break;
            }
        }

        // Remove from block voters
        uint256 blen = blockVoters.length;
        for (uint256 k = 0; k < blen; k++) {
            if (blockVoters[k] == op) {
                blockVoters[k] = blockVoters[blen - 1];
                blockVoters.pop();
                break;
            }
        }

        emit CandidateRemoved(op);
    }

    // ================= ELECTION =================

    /**
     * Start a new election round.
     * Can only be executed once every ELECTION_INTERVAL.
     */
    function startElection() external {
        require(
            block.timestamp >= lastElectionTime + ELECTION_INTERVAL,
            "too early"
        );

        currentElectionId += 1;
        lastElectionTime = block.timestamp;

        for (uint256 i = 0; i < candidateList.length; i++) {
            address op = candidateList[i];
            Candidate storage c = candidates[op];

            c.votes = 0;
            c.lastElectionId = currentElectionId;
            c.isValidator = false;
            c.isBlockVoter = false;
        }

        emit ElectionStarted(currentElectionId, block.timestamp);
    }

    function reStartElection() external onlyOwner {
        currentElectionId += 1;
        lastElectionTime = block.timestamp;

        for (uint256 i = 0; i < candidateList.length; i++) {
            address op = candidateList[i];
            Candidate storage c = candidates[op];

            c.votes = 0;
            c.lastElectionId = currentElectionId;
            c.isValidator = false;
            c.isBlockVoter = false;
        }

        emit ElectionStarted(currentElectionId, block.timestamp);
    }

    /**
     * Vote for a candidate.
     * One address = one vote.
     * Voter must hold sufficient DOL balance.
     */
    function vote(address candidateOp) external {
        require(
            msg.sender.balance >= MIN_VOTER_BALANCE,
            "insufficient voting balance"
        );

        uint256 eid = currentElectionId;
        require(eid > 0, "election not started");
        require(!hasVoted[eid][msg.sender], "already voted");

        Candidate storage c = candidates[candidateOp];
        require(c.exists, "not candidate");

        hasVoted[eid][msg.sender] = true;
        c.votes += 1;

        emit Voted(eid, msg.sender, candidateOp);
    }

    /**
     * Finalize election:
     *  1) Select top 21 block voters
     *  2) From those → select top 7 validators
     */
    function finalizeElection() external onlyOwner {
        uint256 eid = currentElectionId;
        require(eid > 0, "election not started");

        uint256 n = candidateList.length;

        if (n == 0) {
            delete validators;
            delete blockVoters;
            emit ElectionFinalized(eid, validators, blockVoters);
            return;
        }

        uint256 k = n < MAX_BLOCK_VOTERS ? n : MAX_BLOCK_VOTERS;
        address[] memory top21 = _selectTopNByVotes(candidateList, k);

        delete blockVoters;
        for (uint256 i = 0; i < top21.length; i++) {
            blockVoters.push(top21[i]);
            candidates[top21[i]].isBlockVoter = true;
        }

        uint256 m = top21.length < NUM_VALIDATORS
            ? top21.length
            : NUM_VALIDATORS;
        address[] memory top7 = _selectTopNByVotes(top21, m);

        delete validators;
        for (uint256 j = 0; j < top7.length; j++) {
            validators.push(top7[j]);
            candidates[top7[j]].isValidator = true;
        }

        emit ElectionFinalized(eid, validators, blockVoters);
    }

    /**
     * Utility function:
     * Selects top N addresses by vote count.
     * Uses fixed-size "top list" + replace-min strategy (gas efficient for small N).
     */
    function _selectTopNByVotes(
        address[] memory arr,
        uint256 N
    ) internal view returns (address[] memory) {
        if (N == 0 || arr.length == 0) return new address[](0);
        if (N > arr.length) N = arr.length;

        address[] memory topAddrs = new address[](N);
        uint256[] memory topVotes = new uint256[](N);
        uint256 size = 0;

        for (uint256 i = 0; i < arr.length; i++) {
            address op = arr[i];
            uint256 v = candidates[op].votes;

            if (size < N) {
                topAddrs[size] = op;
                topVotes[size] = v;
                size++;
            } else {
                uint256 minIdx = 0;
                uint256 minVal = topVotes[0];
                for (uint256 j = 1; j < N; j++) {
                    if (topVotes[j] < minVal) {
                        minVal = topVotes[j];
                        minIdx = j;
                    }
                }

                if (v > minVal) {
                    topAddrs[minIdx] = op;
                    topVotes[minIdx] = v;
                }
            }
        }

        if (size < N) {
            address[] memory trimmed = new address[](size);
            for (uint256 k = 0; k < size; k++) trimmed[k] = topAddrs[k];
            return trimmed;
        }

        return topAddrs;
    }

    // ================= VIEW HELPERS =================

    function getValidators() external view returns (address[] memory) {
        return validators;
    }

    function getBlockVoters() external view returns (address[] memory) {
        return blockVoters;
    }

    function getCandidates() external view returns (address[] memory) {
        return candidateList;
    }
}
