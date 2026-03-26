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

contract DolphinetGovernance is
    Initializable,
    OwnableUpgradeable,
    GovernanceStorage,
    IGovernance
{
    function initialize(
        address initialOwner,
        address _manager,
        address _delegationManager,
        address _slashingManager
    ) public initializer {
        __Ownable_init(initialOwner);
        manager = _manager;
        delegationManager = IDelegationManager(_delegationManager);
        slashingManager = ISlashingManager(_slashingManager);
        lastElectionTime = block.timestamp - ELECTION_INTERVAL; // Allow immediate election start

        electionFinalized = false;
        finalizedElectionId = 0;
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
        require(
            !candidates[msg.sender].exists ||
                candidates[msg.sender].lastElectionId < currentElectionId,
            "already candidate"
        );

        // requireing candidate to have delegated shares(as operator)
        uint256 shares = delegationManager.getOperatorShares(msg.sender);
        require(
            shares > 0,
            "Governance.registerCandidate: operator has no shares delegated"
        );

        require(
            slashingManager.isOperatorJail(msg.sender) == false,
            "operator is jailed"
        );

        Candidate storage c = candidates[msg.sender];
        c.exists = true;
        c.votes = 0;
        c.lastElectionId = currentElectionId;
        c.isValidator = false;
        c.isBlockVoter = false;

        candidateList.push(msg.sender);

        emit CandidateRegistered(msg.sender, shares);
    }

    function removeCandidate(address op) external onlyManager {
        if (!candidates[op].exists) return;
        _handleRemovalAndShift(op);
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

        _removeFromSet(validators, op, RankRole.VALIDATOR);
        _removeFromSet(blockVoters, op, RankRole.BLOCK_VOTER);
        _removeFromSet(standbyValidators, op, RankRole.STANDBY);

        // Remove from candidateList
        for (uint256 i = 0; i < candidateList.length; i++) {
            if (candidateList[i] == op) {
                candidateList[i] = candidateList[candidateList.length - 1];
                candidateList.pop();
                break;
            }
        }

        // Clear role lookup
        roleOf[op] = RankRole.NONE;
        indexOf[op] = 0;

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

        // Reset election lifecycle flags
        electionFinalized = false;
        finalizedElectionId = 0;

        // Clear previous sets and role lookups
        _clearRankedSets();

        // for (uint256 i = 0; i < candidateList.length; i++) {
        //     address op = candidateList[i];
        //     Candidate storage c = candidates[op];

        //     c.votes = 0;
        //     c.lastElectionId = currentElectionId;
        //     c.isValidator = false;
        //     c.isBlockVoter = false;
        // }

        emit ElectionStarted(currentElectionId, block.timestamp);
    }

    function reStartElection() external onlyOwner {
        currentElectionId += 1;
        lastElectionTime = block.timestamp;

        electionFinalized = false;
        finalizedElectionId = 0;
        _clearRankedSets();

        // for (uint256 i = 0; i < candidateList.length; i++) {
        //     address op = candidateList[i];
        //     Candidate storage c = candidates[op];

        //     c.votes = 0;
        //     c.lastElectionId = currentElectionId;
        //     c.isValidator = false;
        //     c.isBlockVoter = false;
        // }

        emit ElectionStarted(currentElectionId, block.timestamp);
    }

    /**
     * Vote for a candidate.
     * One address = one vote.
     * Voter must hold sufficient DOL balance.
     */
    function vote(address candidateOp) external payable {
        require(msg.value >= MIN_VOTER_BALANCE, "insufficient voting balance");

        uint256 eid = currentElectionId;
        require(eid > 0, "election not started");
        require(!hasVoted[eid][msg.sender], "already voted");

        Candidate storage c = candidates[candidateOp];
        require(
            c.exists && c.lastElectionId == currentElectionId,
            "not a valid candidate"
        );

        // Lock the voting balance for this voter
        voterLockedBalance[msg.sender] += msg.value;

        // Mark the voter as having voted
        hasVoted[eid][msg.sender] = true;

        // Update the candidate's vote count
        c.votes += 1;

        emit Voted(eid, msg.sender, candidateOp);
    }

    /**
     * Claim the locked voting balance.
     * Voter can claim the locked funds after the election.
     */
    function claim() external {
        require(electionFinalized, "cannot claim before election finalized");

        uint256 lockedAmount = voterLockedBalance[msg.sender];
        require(lockedAmount > 0, "no balance to claim");

        // Reset the locked balance
        voterLockedBalance[msg.sender] = 0;

        // Transfer the locked amount back to the voter
        (bool sent, ) = msg.sender.call{value: lockedAmount}("");
        require(sent, "transfer failed");

        emit VoterClaimed(msg.sender, lockedAmount);
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

        // Clear previous ranked sets before writing new ones
        _clearRankedSets();

        if (n == 0) {
            electionFinalized = true;
            finalizedElectionId = eid;
            emit ElectionFinalized(eid, validators, blockVoters);
            emit ElectionFinalizedFlag(eid);
            return;
        }

        // Select Top 28 ranked by votes (or fewer if not enough candidates)
        uint256 k = n < MAX_RANKED ? n : MAX_RANKED;
        address[] memory top = _selectTopNByVotes(candidateList, k);
        _sortByVotesDesc(top);

        // Partition:
        // [0..6]   -> validators (up to 7)
        // [7..20]  -> blockVoters (up to 14)
        // [21..27] -> standbyValidators (up to 7)
        uint256 vCount = top.length < NUM_VALIDATORS
            ? top.length
            : NUM_VALIDATORS;

        // validators
        for (uint256 i = 0; i < vCount; i++) {
            _pushValidator(top[i]);
        }

        // block voters (rank 8-21 = 14 slots)
        if (top.length > NUM_VALIDATORS) {
            uint256 start = NUM_VALIDATORS;
            uint256 end = top.length < MAX_BLOCK_VOTERS
                ? top.length
                : MAX_BLOCK_VOTERS; // 21
            for (uint256 j = start; j < end; j++) {
                _pushBlockVoter(top[j]);
            }
        }

        // standby validators (rank 22-28 = 7 slots)
        if (top.length > MAX_BLOCK_VOTERS) {
            uint256 start2 = MAX_BLOCK_VOTERS; // 21
            uint256 end2 = top.length; // up to 28
            for (uint256 k2 = start2; k2 < end2; k2++) {
                _pushStandby(top[k2]);
            }
            emit StandbyValidatorsSelected(eid, standbyValidators);
        }

        // Mark election finalized
        electionFinalized = true;
        finalizedElectionId = eid;

        // Force-unregister losing candidates (rank > 28)
        // We do it best-effort; do NOT revert the whole finalize if one unregister fails.
        _forceUnregisterLosers();

        emit ElectionFinalized(eid, validators, blockVoters);
        emit ElectionFinalizedFlag(eid);
    }

    function supplementBlockVoters(address[] calldata ops) external onlyOwner {
        require(electionFinalized, "election not finalized");
        require(
            finalizedElectionId == currentElectionId,
            "not current election"
        );

        require(blockVoters.length < MAX_BLOCK_VOTERS, "blockVoters full");

        uint256 remaining = MAX_BLOCK_VOTERS - blockVoters.length;
        require(ops.length <= remaining, "too many ops");

        for (uint256 i = 0; i < ops.length; i++) {
            address op = ops[i];

            // Must be a registered candidate
            require(candidates[op].exists, "not candidate");

            // Must not already be ranked
            require(roleOf[op] == RankRole.NONE, "already ranked");

            // Must still be an eligible operator
            uint256 shares = delegationManager.getOperatorShares(op);
            require(shares > 0, "operator has no shares");

            // Priority: fill validators first
            if (validators.length < NUM_VALIDATORS) {
                _pushValidator(op);
            }
            // Otherwise, fill block voters
            else {
                _pushBlockVoter(op);
            }
        }

        emit SupplementBlockVoters(currentElectionId, ops);
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

    function _forceUnregisterLosers() internal {
        uint256 eid = currentElectionId;

        // Build a quick in-memory mark for ranked addresses
        // NOTE: We avoid storage writes for marking; we just check roleOf after pushing top ranks.
        // Any candidate with roleOf == NONE is out-of-rank and should be force-unregistered.
        for (uint256 i = 0; i < candidateList.length; ) {
            address op = candidateList[i];

            // If ranked (validator/blockVoter/standby), keep it
            if (roleOf[op] != RankRole.NONE) {
                i++;
                continue;
            }

            // Best-effort unregister in DelegationManager to release stake / registration
            // Do not revert if it fails.
            try delegationManager.unRegisterFromGovernance(op) {
                // ok
            } catch {
                emit ForceUnregisterFailed(eid, op);
            }

            // Remove from governance candidate list as well
            _removeCandidate(op);
            // No increment of i: the last element was swapped into current index i,
            // so we must check the new candidateList[i] in the next iteration.
        }
    }

    function _handleRemovalAndShift(address op) internal {
        RankRole role = roleOf[op];

        // First, remove candidate record + remove from lists (if present)
        _removeCandidate(op);

        // If not in ranked sets, nothing to shift
        if (role == RankRole.NONE) return;

        // Shift rules:
        // - If a validator removed: promote first standby -> validators
        // - If a block voter removed: promote first standby -> blockVoters
        // - If a standby removed: no promotion
        if (role == RankRole.VALIDATOR) {
            _promoteStandbyToValidator();
        } else if (role == RankRole.BLOCK_VOTER) {
            _promoteStandbyToBlockVoter();
        }
    }

    function _promoteStandbyToValidator() internal {
        if (standbyValidators.length == 0) return;

        address op = _popHighestVoteStandby();

        // Validators are a strict subset of block voters
        _pushValidator(op);
    }

    function _promoteStandbyToBlockVoter() internal {
        if (standbyValidators.length == 0) return;

        address op = _popHighestVoteStandby();
        _pushBlockVoter(op);
    }

    function _removeFromSet(
        address[] storage set,
        address op,
        RankRole role
    ) internal {
        if (roleOf[op] != role) return;

        uint256 idx = indexOf[op];
        uint256 last = set.length - 1;

        if (idx != last) {
            address swapped = set[last];
            set[idx] = swapped;
            indexOf[swapped] = idx;
        }

        set.pop();
        roleOf[op] = RankRole.NONE;
        indexOf[op] = 0;
    }

    function _pushValidator(address op) internal {
        // set candidate flags
        candidates[op].isValidator = true;
        candidates[op].isBlockVoter = true; // validators are also block voters conceptually
        // push
        roleOf[op] = RankRole.VALIDATOR;
        indexOf[op] = validators.length;
        validators.push(op);
    }

    function _pushBlockVoter(address op) internal {
        candidates[op].isValidator = false;
        candidates[op].isBlockVoter = true;

        roleOf[op] = RankRole.BLOCK_VOTER;
        indexOf[op] = blockVoters.length;
        blockVoters.push(op);
    }

    function _pushStandby(address op) internal {
        candidates[op].isValidator = false;
        candidates[op].isBlockVoter = false; // standby is neither validator nor block voter
        roleOf[op] = RankRole.STANDBY;
        indexOf[op] = standbyValidators.length;
        standbyValidators.push(op);
    }

    function _popHighestVoteStandby() internal returns (address op) {
        uint256 bestIdx = 0;
        uint256 bestVotes = candidates[standbyValidators[0]].votes;

        for (uint256 i = 1; i < standbyValidators.length; i++) {
            uint256 v = candidates[standbyValidators[i]].votes;
            if (v > bestVotes) {
                bestVotes = v;
                bestIdx = i;
            }
        }

        op = standbyValidators[bestIdx];

        // swap & pop
        uint256 last = standbyValidators.length - 1;
        if (bestIdx != last) {
            address swapped = standbyValidators[last];
            standbyValidators[bestIdx] = swapped;
            indexOf[swapped] = bestIdx;
        }

        standbyValidators.pop();
        roleOf[op] = RankRole.NONE;
        indexOf[op] = 0;
    }

    function _clearRankedSets() internal {
        // Clear roleOf/indexOf for all currently ranked addresses
        for (uint256 i = 0; i < validators.length; i++) {
            address op = validators[i];
            roleOf[op] = RankRole.NONE;
            indexOf[op] = 0;
            candidates[op].isValidator = false;
            candidates[op].isBlockVoter = false;
        }
        for (uint256 j = 0; j < blockVoters.length; j++) {
            address op2 = blockVoters[j];
            roleOf[op2] = RankRole.NONE;
            indexOf[op2] = 0;
            candidates[op2].isValidator = false;
            candidates[op2].isBlockVoter = false;
        }
        for (uint256 k = 0; k < standbyValidators.length; k++) {
            address op3 = standbyValidators[k];
            roleOf[op3] = RankRole.NONE;
            indexOf[op3] = 0;
            candidates[op3].isValidator = false;
            candidates[op3].isBlockVoter = false;
        }

        delete validators;
        delete blockVoters;
        delete standbyValidators;
    }

    function _sortByVotesDesc(address[] memory arr) internal view {
        uint256 len = arr.length;
        for (uint256 i = 0; i < len; i++) {
            for (uint256 j = i + 1; j < len; j++) {
                if (candidates[arr[j]].votes > candidates[arr[i]].votes) {
                    address tmp = arr[i];
                    arr[i] = arr[j];
                    arr[j] = tmp;
                }
            }
        }
    }

    // ================= VIEW HELPERS =================

    function getValidators() public view returns (address[] memory) {
        return validators;
    }

    function getBlockVoters() external view returns (address[] memory) {
        return blockVoters;
    }

    function getCandidates() external view returns (address[] memory) {
        return candidateList;
    }

    function getStandbyValidators() external view returns (address[] memory) {
        return standbyValidators;
    }

    function isElectionFinalized() external view returns (bool, uint256) {
        return (electionFinalized, finalizedElectionId);
    }

    function getAllBlockVoters() external view returns (address[] memory) {
        uint256 total = validators.length + blockVoters.length;
        address[] memory all = new address[](total);

        for (uint256 i = 0; i < validators.length; i++) {
            all[i] = validators[i];
        }
        for (uint256 j = 0; j < blockVoters.length; j++) {
            all[validators.length + j] = blockVoters[j];
        }
        return all;
    }

    function getValidatorsShares()
        external
        view
        returns (address[] memory, uint256[] memory)
    {
        uint256 len = validators.length;
        uint256[] memory shares = new uint256[](len);
        address[] memory validators = getValidators();

        for (uint256 i = 0; i < len; i++) {
            shares[i] = delegationManager.getOperatorShares(validators[i]);
        }
        return (validators, shares);
    }
}
