# POS External Usage Guide

This document is for external integrators who need to use the POS contracts after deployment.

It focuses on:

- which contract to call for each action
- the expected call order
- the roles that are allowed to call each function
- common read methods used by frontends and backends

## Contract Map

The main POS contracts are:

- `PauserRegistry`: pause and unpause permissions
- `FdChainBase`: core share accounting and vault logic
- `FdChainDepositManager`: user deposit entrypoint
- `DelegationManager`: operator registration, delegation, undelegation, queued withdrawals
- `SlashingManager`: jail and slash operators, withdraw slashed funds
- `DolphinetGovernance`: candidate registration, elections, voting
- `RewardManager`: fee distribution and reward claiming

For most external users, the main entrypoints are:

- `FdChainDepositManager`
- `DelegationManager`
- `DolphinetGovernance`
- `RewardManager`

## Recommended User Flows

### 1. Stake into the POS pool

Call `FdChainDepositManager.depositIntoDolphinnetChain(amount)` and send `msg.value == amount`.

Relevant contract and methods:

- `FdChainDepositManager.depositIntoDolphinnetChain(uint256 amount)`
- `FdChainDepositManager.getDeposits(address staker)`
- `FdChainBase.sharesToUnderlyingView(uint256 shares)`
- `FdChainBase.userUnderlyingView(address user)`

Important behavior:

- the call reverts if `amount != msg.value`
- deposits are paused if the deposit manager is paused
- the deposit manager forwards funds into `FdChainBase`
- the user receives shares, not a transferable token
- the deployed script currently initializes `FdChainBase` with:
  - `minDeposit = 320000 * 1e18`
  - `maxDeposit = 1820000 * 1e18`

### 2. Stake for another address by signature

Call `FdChainDepositManager.depositIntoDolphinnetChainWithSignature(amount, staker, expiry, signature)` and send `msg.value == amount`.

Relevant contract and methods:

- `FdChainDepositManager.depositIntoDolphinnetChainWithSignature(uint256,address,uint256,bytes)`
- `DelegationManager.domainSeparator()` if you need related signature context elsewhere

Important behavior:

- the signature must still be valid at `expiry`
- the staker nonce is consumed during the call
- the shares are credited to `staker`

### 3. Register as an operator

Call `DelegationManager.registerAsOperator(operatorDetails, nodeUrl)`.

Relevant contract and methods:

- `DelegationManager.registerAsOperator(OperatorDetails,string)`
- `DelegationManager.modifyOperatorDetails(OperatorDetails)`
- `DelegationManager.updateOperatorNodeUrl(string)`
- `DelegationManager.operatorDetails(address operator)`
- `DelegationManager.getOperatorShares(address operator)`

`OperatorDetails` fields:

- `earningsReceiver`
- `delegationApprover`
- `stakerOptOutWindowBlocks`

Important behavior:

- an address can only register once
- registration self-delegates the operator to itself
- the contract currently does not enforce the commented-out minimum deposit check in `registerAsOperator`

### 4. Delegate to an operator

Call `DelegationManager.delegateTo(operator, approverSignatureAndExpiry, approverSalt)`.

Relevant contract and methods:

- `DelegationManager.delegateTo(address,SignatureWithExpiry,bytes32)`
- `DelegationManager.delegateToBySignature(address,address,SignatureWithExpiry,SignatureWithExpiry,bytes32)`
- `DelegationManager.delegatedTo(address staker)`
- `DelegationManager.isDelegated(address staker)`
- `DelegationManager.getOperatorDelegatedStakers(address operator)`

Important behavior:

- the staker should already hold shares in `FdChainDepositManager`
- delegated shares increase automatically when the staker deposits more later
- delegated shares decrease automatically during withdrawals and slashing

### 5. Undelegate or withdraw shares

There are two related flows:

- `undelegate(staker)` removes delegation and queues a withdrawal for the staker's full delegated position
- `queueWithdrawals(params)` queues a partial withdrawal

Relevant contract and methods:

- `DelegationManager.undelegate(address staker)`
- `DelegationManager.queueWithdrawals(QueuedWithdrawalParams[] calldata)`
- `DelegationManager.completeQueuedWithdrawal(Withdrawal calldata)`
- `DelegationManager.completeQueuedWithdrawals(Withdrawal[] calldata)`
- `DelegationManager.cumulativeWithdrawalsQueued(address staker)`
- `DelegationManager.calculateWithdrawalRoot(Withdrawal memory withdrawal)`

Important behavior:

- queued withdrawals are subject to `chainBaseWithdrawalDelayBlock`
- only the withdrawer can queue their own withdrawals in the current implementation
- queued withdrawals are completed later by submitting the `Withdrawal` struct back to the contract

### 6. Register as a governance candidate

Call `DolphinetGovernance.registerCandidate()`.

Relevant contract and methods:

- `DolphinetGovernance.registerCandidate()`
- `DolphinetGovernance.getCandidates()`
- `DolphinetGovernance.getValidators()`
- `DolphinetGovernance.getBlockVoters()`
- `DolphinetGovernance.getStandbyValidators()`

Important behavior:

- the caller must have delegated operator shares
- jailed operators cannot register
- the same address cannot register twice in the same election

### 7. Start an election and vote

Election flow:

1. call `startElection()`
2. candidates register
3. users call `vote(candidateOp)` with native token
4. owner calls `finalizeElection()`
5. voters call `claim()` to recover their locked voting balance

Relevant contract and methods:

- `DolphinetGovernance.startElection()`
- `DolphinetGovernance.vote(address candidateOp)`
- `DolphinetGovernance.finalizeElection()`
- `DolphinetGovernance.claim()`
- `DolphinetGovernance.isElectionFinalized()`
- `DolphinetGovernance.getValidatorsShares()`

Important behavior:

- voting currently requires `msg.value >= 0.001 ether`
- one address can vote once per election
- vote weight is currently one vote per address, not proportional to `msg.value`
- `finalizeElection()` is `onlyOwner`
- after finalization, losers outside the top ranked set are force-unregistered from governance on a best-effort basis

### 8. Claim rewards

Operators and stakers claim from `RewardManager`.

Relevant contract and methods:

- `RewardManager.operatorClaimReward()`
- `RewardManager.stakeHolderClaimReward(address chainBase)`
- `RewardManager.getStakeHolderAmount(address chainBase)`

Important behavior:

- rewards are paid in native token held by `RewardManager`
- `payFee()` is not for normal end users; it is restricted to `payFeeManager`
- `updateStakePercent()` is restricted to `rewardManager`

## Admin and Operator Roles

### PauserRegistry

Relevant methods:

- `PauserRegistry.setIsPauser(address,bool)`
- `PauserRegistry.setUnpauser(address)`

Behavior:

- only `unpauser` can manage pausers and change the unpauser

### Pausable contracts

The following contracts use `Pausable`:

- `FdChainBase`
- `FdChainDepositManager`
- `DelegationManager`
- `RewardManager`

Relevant methods:

- `pause(uint256 newPausedStatus)`
- `pauseAll()`
- `unpause(uint256 newPausedStatus)`
- `unpauseAll()`

Behavior:

- only a registered pauser can pause
- only the registry `unpauser` can unpause

### SlashingManager

Relevant methods:

- `jail(address operator)`
- `unJail(address operator)`
- `freezeAndSlashingShares(address operator,uint256 slashShare)`
- `updateSlashingRecipient(address)`
- `withdraw()`

Behavior:

- `jail`, `unJail`, `freezeAndSlashingShares`, and `updateSlashingRecipient` are restricted to `slasherAddress`
- `withdraw()` is publicly callable, but funds always go to `slashingRecipient`

### Governance owner vs manager

Relevant methods:

- `setManager(address)`
- `removeCandidate(address)`

Behavior:

- `owner` can finalize elections and set manager
- `manager` can remove a candidate
- in the current deployment script, both are initialized to the deployer address unless the script is customized before deployment

## Common Read Methods

For frontends, dashboards, and indexers, these are the most useful view methods:

### Deposits and shares

- `FdChainDepositManager.getDeposits(address staker)`
- `FdChainDepositManager.stakerFdChainBaseShares(address staker)`
- `FdChainBase.totalShares()`
- `FdChainBase.shares(address user)`
- `FdChainBase.userUnderlyingView(address user)`
- `FdChainBase.getDepositLimits()`

### Delegation

- `DelegationManager.delegatedTo(address staker)`
- `DelegationManager.isDelegated(address staker)`
- `DelegationManager.isOperator(address operator)`
- `DelegationManager.operatorDetails(address operator)`
- `DelegationManager.getOperatorShares(address operator)`
- `DelegationManager.getStakerSharesOfOperator(address operator)`
- `DelegationManager.getOperatorDelegatedStakers(address operator)`

### Governance

- `DolphinetGovernance.getCandidates()`
- `DolphinetGovernance.getValidators()`
- `DolphinetGovernance.getBlockVoters()`
- `DolphinetGovernance.getStandbyValidators()`
- `DolphinetGovernance.isElectionFinalized()`
- `DolphinetGovernance.getValidatorsShares()`

### Rewards

- `RewardManager.getStakeHolderAmount(address chainBase)`
- `RewardManager.operatorRewards(address operator)`

### Slashing

- `SlashingManager.isOperatorJail(address operator)`
- `SlashingManager.slashingRecipient()`
- `SlashingManager.totalProcessedSlashingAmount()`

## Suggested Integration Order

If you are integrating as a wallet, frontend, or SDK, this order usually works best:

1. read deployed contract addresses
2. read `PauserRegistry` and current pause state
3. read deposit limits from `FdChainBase`
4. let the user deposit through `FdChainDepositManager`
5. optionally register as operator through `DelegationManager`
6. delegate to an operator through `DelegationManager`
7. monitor rewards and queued withdrawals
8. monitor governance election state if your app includes validator voting

## Deployment-Time Checks Before Mainnet Use

The current `DeployPOS.s.sol` has had the obvious initialization mismatches fixed, but you should still confirm the deployed state after broadcasting:

1. `PauserRegistry.unpauser()` is the expected address
2. `DelegationManager.fdChainDepositManager()` points to the deployed deposit manager
3. `SlashingManager.slasherAddress()` is the expected slasher
4. `RewardManager.payFeeManager()` and `RewardManager.rewardManager()` are the intended operational addresses
5. `DolphinetGovernance.owner()` and `DolphinetGovernance.manager()` are the intended admin addresses
6. each proxy admin owner is the expected operational account or multisig

## Notes

- compile is currently passing locally
- I was not able to complete `forge test` in this environment because the local Foundry binary panics while creating the external trace/signature client on macOS
- because of that, a final dry-run deployment on your target RPC is still strongly recommended before production broadcast
