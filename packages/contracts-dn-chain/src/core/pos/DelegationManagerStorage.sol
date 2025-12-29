// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.24;

import "../../interfaces/IFdChainDepositManager.sol";
import "../../interfaces/IDelegationManager.sol";
import "../../interfaces/ISlashingManager.sol";

abstract contract DelegationManagerStorage is IDelegationManager {
    bytes32 public constant DOMAIN_TYPEHASH =
        keccak256(
            "EIP712Domain(string name,uint256 chainId,address verifyingContract)"
        );

    bytes32 public constant STAKER_DELEGATION_TYPEHASH =
        keccak256(
            "StakerDelegation(address staker,address operator,uint256 nonce,uint256 expiry)"
        );

    bytes32 public constant DELEGATION_APPROVAL_TYPEHASH =
        keccak256(
            "DelegationApproval(address staker,address operator,address delegationApprover,bytes32 salt,uint256 expiry)"
        );

    bytes32 internal _DOMAIN_SEPARATOR;

    IFdChainDepositManager public fdChainDepositManager;

    IFdChainBase public fdChainBase;

    ISlashingManager public slashingManager;

    uint256 public constant MAX_WITHDRAWAL_DELAY_BLOCKS = 216000;

    uint256 public chainBaseWithdrawalDelayBlock;

    address[] public stakerList;

    mapping(address => uint256) public operatorShares;

    mapping(address => mapping(address => uint256))
        public stakerDelegateSharesToOperator;

    mapping(address => OperatorDetails) internal _operatorDetails;

    mapping(address => address) public delegatedTo;

    mapping(address => uint256) public stakerNonce;

    mapping(address => mapping(bytes32 => bool))
        public delegationApproverSaltIsSpent;

    mapping(bytes32 => bool) public pendingWithdrawals;

    mapping(address => uint256) public cumulativeWithdrawalsQueued;

    mapping(address => address[]) public operatorDelegatedStakers;

    function _initializeDelegationManagerStorage(
        IFdChainDepositManager _fdChainDepositManager,
        IFdChainBase _fdChainBase,
        ISlashingManager _slashingManager
    ) internal {
        fdChainDepositManager = _fdChainDepositManager;
        fdChainBase = _fdChainBase;
        slashingManager = _slashingManager;
    }

    uint256[100] private __gap;
}
