// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.24;

import "@openzeppelin-upgrades/contracts/proxy/utils/Initializable.sol";
import "@openzeppelin-upgrades/contracts/access/OwnableUpgradeable.sol";
import "@openzeppelin-upgrades/contracts/utils/ReentrancyGuardUpgradeable.sol";

import "./ChainDepositManagerStorage.sol";
import "../../libraries/EIP1271SignatureUtils.sol";
import "@/access/Pausable.sol";

contract FdChainDepositManager is
    Initializable,
    OwnableUpgradeable,
    ReentrancyGuardUpgradeable,
    Pausable,
    FdChainDepositManagerStorage
{
    uint8 internal constant PAUSED_DEPOSITS = 0;
    uint256 internal ORIGINAL_CHAIN_ID;

    modifier onlyDelegationManager() {
        require(msg.sender == address(delegation), "onlyDelegationManager");
        _;
    }

    constructor() {
        _disableInitializers();
    }

    function initialize(
        address initialOwner,
        IDelegationManager _delegation,
        IFdChainBase _FdChainBase
    ) public initializer {
        _DOMAIN_SEPARATOR = _calculateDomainSeparator();
        __Ownable_init(initialOwner);
        _initFdChainDepositManagerStorage(_delegation, _FdChainBase);

        ORIGINAL_CHAIN_ID = block.chainid;
    }

    receive() external payable {}

    function depositIntoDolphinnetChain(
        uint256 amount
    ) external payable whenNotPaused nonReentrant returns (uint256 shares) {
        require(amount == msg.value, "deposit value not match amount");
        shares = _depositIntoDolphinnetChain(msg.sender, amount);
    }

    function depositIntoDolphinnetChainWithSignature(
        uint256 amount,
        address staker,
        uint256 expiry,
        bytes memory signature
    ) external payable whenNotPaused nonReentrant returns (uint256 shares) {
        require(amount == msg.value, "deposit value not match amount");
        require(
            expiry >= block.timestamp,
            "FdChainDepositManager.depositIntoDolphinnetChainWithSignature: signature expired"
        );
        uint256 nonce = nonces[staker];

        bytes32 structHash = keccak256(
            abi.encode(DEPOSIT_TYPEHASH, staker, amount, nonce, expiry)
        );

        unchecked {
            nonces[staker] = nonce + 1;
        }

        bytes32 digestHash = keccak256(
            abi.encodePacked("\x19\x01", domainSeparator(), structHash)
        );

        EIP1271SignatureUtils.checkSignature_EIP1271(
            staker,
            digestHash,
            signature
        );

        shares = _depositIntoDolphinnetChain(staker, amount);
    }

    function removeShares(
        address staker,
        uint256 shareAmount
    ) external onlyDelegationManager {
        require(
            shareAmount != 0,
            "FdChainDepositManager.removeShares: shareAmount should not be zero!"
        );

        uint256 userShares = stakerFdChainBaseShares[staker];

        require(
            shareAmount <= userShares,
            "FdChainDepositManager._removeShares: shareAmount too high"
        );

        unchecked {
            userShares = userShares - shareAmount;
        }

        stakerFdChainBaseShares[staker] = userShares;

        if (userShares == 0) {
            delete stakerFdChainBaseShares[staker];
            FdChainBase.deleteStaker(staker);
        }
    }

    function addShares(
        address staker,
        uint256 shares
    ) external onlyDelegationManager {
        _addShares(staker, shares);
    }

    function withdrawSharesAsDol(
        address recipient,
        uint256 shares
    ) external onlyDelegationManager {
        FdChainBase.withdraw(recipient, shares);
    }

    function getDeposits(address staker) external view returns (uint256) {
        return stakerFdChainBaseShares[staker];
    }

    function domainSeparator() public view returns (bytes32) {
        if (block.chainid == ORIGINAL_CHAIN_ID) {
            return _DOMAIN_SEPARATOR;
        } else {
            return _calculateDomainSeparator();
        }
    }

    // ================= internal function =================
    function _depositIntoDolphinnetChain(
        address staker,
        uint256 amount
    ) internal returns (uint256 shares) {
        shares = FdChainBase.deposit{value: amount}(amount, staker);

        _addShares(staker, shares);

        delegation.increaseDelegatedShares(staker, shares);

        return shares;
    }

    function _addShares(address staker, uint256 shares) internal {
        require(
            staker != address(0),
            "FdChainDepositManager._addShares: staker cannot be zero address"
        );
        require(
            shares != 0,
            "FdChainDepositManager._addShares: shares should not be zero!"
        );
        stakerFdChainBaseShares[staker] += shares;
        emit Deposit(staker, FdChainBase, shares);
    }

    function _calculateDomainSeparator() internal view returns (bytes32) {
        return
            keccak256(
                abi.encode(
                    DOMAIN_TYPEHASH,
                    keccak256(bytes("dolphinnetChain")),
                    block.chainid,
                    address(this)
                )
            );
    }
}
