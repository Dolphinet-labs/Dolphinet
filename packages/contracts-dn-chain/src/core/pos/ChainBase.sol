// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.24;

import "@/access/Pausable.sol";

import "@openzeppelin-upgrades/contracts/proxy/utils/Initializable.sol";

import "../../interfaces/IFdChainBase.sol";
import "../../interfaces/IFdChainDepositManager.sol";

contract FdChainBase is Initializable, IFdChainBase, Pausable {
    uint8 internal constant PAUSED_DEPOSITS = 0;
    uint8 internal constant PAUSED_WITHDRAWALS = 1;

    uint256 internal constant SHARES_OFFSET = 1e3;
    uint256 internal constant BALANCE_OFFSET = 1e3;

    uint256 internal constant MAX_STAKER_NUMBERS = 320;

    address[] stakerList;

    mapping(address => bool) public stakerExistInList;

    IFdChainDepositManager public FdChainDepositManager;

    uint256 public minDeposit;
    uint256 public maxDeposit;

    uint256 public totalShares;
    uint256 public stakerNumbers;

    modifier onlyStrategyManager() {
        require(
            msg.sender == address(FdChainDepositManager),
            "FdChainBase.onlyStrategyManager"
        );
        _;
    }

    modifier onlyStakerNumbersLessThanMaxLimit() {
        require(
            stakerNumbers < MAX_STAKER_NUMBERS,
            "Stakers too much in this pool"
        );
        _;
    }

    constructor() {
        _disableInitializers();
    }

    function initialize(
        IPauserRegistry _pauserRegistry,
        uint256 _minDeposit,
        uint256 _maxDeposit,
        IFdChainDepositManager _FdChainDepositManager
    ) public virtual initializer {
        _setDepositLimits(_minDeposit, _maxDeposit);
        _initializeFdChainBase(_pauserRegistry);
        FdChainDepositManager = _FdChainDepositManager;
    }

    function _initializeFdChainBase(
        IPauserRegistry _pauserRegistry
    ) internal onlyInitializing {
        _initializePauser(_pauserRegistry, UNPAUSE_ALL);
    }

    function deposit(
        uint256 amount,
        address staker
    )
        external
        payable
        virtual
        override
        onlyStrategyManager
        onlyStakerNumbersLessThanMaxLimit
        returns (uint256 newShares)
    {
        require(
            amount >= minDeposit,
            "FdChainBase: deposit token must more than min deposit amount"
        );
        require(
            amount <= maxDeposit,
            "FdChainBase: deposit token must less than max deposit amount"
        );

        uint256 priorTotalShares = totalShares;

        uint256 virtualShareAmount = priorTotalShares + SHARES_OFFSET;
        uint256 virtualTokenBalance = dolBalance() + BALANCE_OFFSET;

        uint256 virtualPriorTokenBalance = virtualTokenBalance - amount;
        newShares = (amount * virtualShareAmount) / virtualPriorTokenBalance;

        require(
            newShares != 0,
            "FdChainBase.deposit: new shares cannot be zero"
        );

        totalShares = (priorTotalShares + newShares);

        if (!stakerExistInList[staker]) {
            unchecked {
                stakerNumbers = stakerNumbers + 1;
            }
            stakerList.push(staker);
            stakerExistInList[staker] = true;
        }

        return newShares;
    }

    function withdraw(
        address recipient,
        uint256 amountShares
    ) external virtual override whenNotPaused onlyStrategyManager {
        uint256 priorTotalShares = totalShares;
        require(
            amountShares <= priorTotalShares,
            "FdChainBase.withdraw: amountShares must be less than or equal to totalShares"
        );

        uint256 virtualPriorTotalShares = priorTotalShares + SHARES_OFFSET;
        uint256 virtualTokenBalance = dolBalance() + BALANCE_OFFSET;

        uint256 amountToSend = (virtualTokenBalance * amountShares) /
            virtualPriorTotalShares;

        totalShares = priorTotalShares - amountShares;

        _afterWithdrawal(recipient, amountToSend);
    }

    function deleteStaker(address staker) public onlyStrategyManager {
        uint256 length = stakerList.length;
        for (uint256 i = 0; i < length; i++) {
            if (stakerList[i] == staker) {
                stakerList[i] = stakerList[stakerNumbers - 1];
                stakerList.pop();
                stakerNumbers = stakerNumbers - 1;
                stakerExistInList[staker] = false;
                break;
            }
        }
    }

    function _afterWithdrawal(
        address recipient,
        uint256 amountToSend
    ) internal virtual {
        (bool success, ) = payable(recipient).call{value: amountToSend}("");
        require(success, "FdChainBase._afterWithdrawal: transfer dol failed");
    }

    function explanation()
        external
        pure
        virtual
        override
        returns (string memory)
    {
        return "Dolphinnet Chain Pos Staking Protocol";
    }

    function sharesToUnderlyingView(
        uint256 amountShares
    ) public view virtual override returns (uint256) {
        uint256 virtualTotalShares = totalShares + SHARES_OFFSET;
        uint256 virtualTokenBalance = dolBalance() + BALANCE_OFFSET;
        return (virtualTokenBalance * amountShares) / virtualTotalShares;
    }

    function sharesToUnderlying(
        uint256 amountShares
    ) public view virtual override returns (uint256) {
        return sharesToUnderlyingView(amountShares);
    }

    function underlyingToSharesView(
        uint256 amountUnderlying
    ) public view virtual returns (uint256) {
        uint256 virtualTotalShares = totalShares + SHARES_OFFSET;
        uint256 virtualTokenBalance = dolBalance() + BALANCE_OFFSET;
        return (amountUnderlying * virtualTotalShares) / virtualTokenBalance;
    }

    function underlyingToShares(
        uint256 amountUnderlying
    ) external view virtual returns (uint256) {
        return underlyingToSharesView(amountUnderlying);
    }

    function userUnderlyingView(
        address user
    ) external view virtual returns (uint256) {
        return sharesToUnderlyingView(shares(user));
    }

    function userUnderlying(address user) external virtual returns (uint256) {
        return sharesToUnderlying(shares(user));
    }

    function shares(address user) public view virtual returns (uint256) {
        return FdChainDepositManager.stakerFdChainBaseShares(user);
    }

    function setDepositLimits(
        uint256 newMinDeposit,
        uint256 newMaxDeposit
    ) external onlyStrategyManager {
        _setDepositLimits(newMinDeposit, newMaxDeposit);
    }

    function getDepositLimits() external view returns (uint256, uint256) {
        return (minDeposit, maxDeposit);
    }

    function _setDepositLimits(
        uint256 newMinDeposit,
        uint256 newMaxDeposit
    ) internal {
        emit MinDepositUpdated(minDeposit, newMinDeposit);
        emit MaxDepositUpdated(maxDeposit, newMaxDeposit);
        require(
            minDeposit <= newMaxDeposit,
            "FdChainBase._setDepositLimits: minDeposit must less than maxDeposit"
        );
        minDeposit = newMinDeposit;
        maxDeposit = newMaxDeposit;
    }

    function dolBalance() internal view virtual returns (uint256) {
        return address(this).balance;
    }

    function stakerListLength() external view returns (uint256) {
        return stakerList.length;
    }

    function stakerListFind(uint256 index) external view returns (address) {
        return stakerList[index];
    }

    uint256[100] private __gap;
}
