// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"errors"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

var (
	errInsufficientBalanceForGas = errors.New("insufficient balance to pay for gas")
)

/*
The State Transitioning Model

A state transition is a change made when a transaction is applied to the current world state
The state transitioning model does all the necessary work to work out a valid new state root.

1) Nonce handling
2) Pre pay gas
3) Create a new state object if the recipient is \0*32
4) Value transfer
== If contract creation ==
  4a) Attempt to run transaction data
  4b) If valid, use result as code for the new state object
== end ==
5) Run Script section
6) Derive new state root
*/
type StateTransition struct {
	gp         *GasPool
	msg        Message
	gas        uint64
	gasPrice   *big.Int
	initialGas uint64
	value      *big.Int
	data       []byte
	state      vm.StateDB
	// *vm是指针，从core/vm中拿虚拟机？
	evm        *vm.EVM
}

// Message represents a message sent to a contract.
// 这里的message貌似是调用contract的transaction
type Message interface {
	From() common.Address
	//FromFrontier() (common.Address, error)
	To() *common.Address

	GasPrice() *big.Int
	// 这里是什么意思？
	Gas() uint64
	Value() *big.Int

	Nonce() uint64
	CheckNonce() bool
	Data() []byte
}

// IntrinsicGas computes the 'intrinsic gas' for a message with the given data.
// 1. 固有gas的成本，合约创建起步价53000gas、合约调用21000gas
func IntrinsicGas(data []byte, contractCreation, homestead bool) (uint64, error) {
	// Set the starting gas for the raw transaction
	var gas uint64
	if contractCreation && homestead {
		gas = params.TxGasContractCreation
	} else {
		gas = params.TxGas
	}
	// Bump the required gas by the amount of transactional data
	if len(data) > 0 {
		// Zero and non-zero bytes are priced differently
		// 2. 计算transaction中input的非零字节和零字节，
		var nz uint64
		for _, byt := range data {
			if byt != 0 {
				nz++
			}
		}
		// Make sure we don't exceed uint64 for all data combinations
		// 3. EIP2118之前，零字节4gas、非零字节68gas（EIP2028改为16）
		// 零字节通过ELP编码协议可以压缩0字节，向Trie存储数据时，零字节占用空间更少
		if (math.MaxUint64-gas)/params.TxDataNonZeroGas < nz {
			return 0, vm.ErrOutOfGas
		}
		gas += nz * params.TxDataNonZeroGas

		z := uint64(len(data)) - nz
		if (math.MaxUint64-gas)/params.TxDataZeroGas < z {
			return 0, vm.ErrOutOfGas
		}
		// 4. 检查是否整数溢出，同时把所消耗的gas加总
		gas += z * params.TxDataZeroGas
	}
	return gas, nil
}

// NewStateTransition initialises and returns a new state transition object.
// 1. 传入evm对象的指针，这里返回一个 new state transition object（就是st * StateTransition）
func NewStateTransition(evm *vm.EVM, msg Message, gp *GasPool) *StateTransition {
	return &StateTransition{
		gp:       gp,
		// 2. evm对象的指针
		evm:      evm,
		msg:      msg,
		gasPrice: msg.GasPrice(),
		value:    msg.Value(),
		data:     msg.Data(),
		// 3. 底层数据库：状态树
		state:    evm.StateDB,
	}
}

// ApplyMessage computes the new state by applying the given message
// against the old state within the environment.
//
// ApplyMessage returns the bytes returned by any EVM execution (if it took place),
// the gas used (which includes gas refunds) and an error if it failed. An error always
// indicates a core error meaning that the message would always fail for that particular
// state and would never be accepted within a block.
func ApplyMessage(evm *vm.EVM, msg Message, gp *GasPool) ([]byte, uint64, bool, error) {
	// 处理每一笔交易：调用了这个NewStateTransition
	return NewStateTransition(evm, msg, gp).TransitionDb()
}

// to returns the recipient of the message.
func (st *StateTransition) to() common.Address {
	if st.msg == nil || st.msg.To() == nil /* contract creation */ {
		return common.Address{}
	}
	return *st.msg.To()
}

func (st *StateTransition) useGas(amount uint64) error {
	if st.gas < amount {
		return vm.ErrOutOfGas
	}
	// st.gas减去当前使用的gas amount（就是固有成本的gas）
	st.gas -= amount

	return nil
}

// preCheck中检查gas的函数
func (st *StateTransition) buyGas() error {
	// 1.减去对应的eth = gas数量 * gas价格
	// msg.Gas()其实就是gas limit，用 msg.Gas() * gasPrice，即gas的数量 * gasPrice（伦敦升级之前的gas计算方法）
	// 所以这里的 msg.Gas() 就是把 stateTransaction 中的gas pool = gasl imit拿过来
	// 小插曲，bigint = int64

	// := 在golang中是声明变量并赋值，不需要定义类型
	mgval := new(big.Int).Mul(new(big.Int).SetUint64(st.msg.Gas()), st.gasPrice)

	// 2.判断当前账户的余额是否足够支付gas
	// 所以GetBalance这个就是查询账户余额的函数？
	// st.state是在某一个state或某一个block的意思吗？evm存储之前的archive吗，还是遍历每一个block的时候都看当前block的这个数据 所以不需要存储历史余额
	if st.state.GetBalance(st.msg.From()).Cmp(mgval) < 0 {
		return errInsufficientBalanceForGas
	}
	// 3. 从整个block的gas pool里扣除这个交易预计消耗的gas数量
	if err := st.gp.SubGas(st.msg.Gas()); err != nil {
		return err
	}
	// 4. 把所有的 msg.gas 都累加到 st.gas 中
	// 这里会在后面的evm执行中被不断的扣除
	st.gas += st.msg.Gas()
	// 5. initialGas记录初始的gas
	st.initialGas = st.msg.Gas()
	// 6. 从发起者的address中扣除对应的eth（如果出错可能会回滚），调用mgval 减去对应gas
	// gas计算方法：https://coindollarpay.com/%E4%BB%A5%E5%A4%AA%E5%9D%8Agas%E6%80%8E%E4%B9%88%E8%AE%A1%E7%AE%97/
	st.state.SubBalance(st.msg.From(), mgval)
	return nil
}

func (st *StateTransition) preCheck() error {
	// Make sure this transaction's nonce is correct.
	// 检查这笔交易的随机数是否正确，nonce必须 = st.msg.Nonce()
	if st.msg.CheckNonce() {
		nonce := st.state.GetNonce(st.msg.From())
		if nonce < st.msg.Nonce() {
			return ErrNonceTooHigh
		} else if nonce > st.msg.Nonce() {
			return ErrNonceTooLow
		}
	}
	// 返回buy gas，去看一看这是什么东西，就是发送账户需要减去gas（gas有点像从矿工那里买计算机的处理资源，所以叫buy gas）
	return st.buyGas()
}

// TransitionDb will transition the state by applying the current message and
// returning the result including the used gas. It returns an error if failed.
// An error indicates a consensus issue.

// TransitionDb通过处理每一笔message还有gas的一些逻辑，来进行state的转移
// 如果失败了是一些共识的问题（consensus issue）？

// 1. evm中比较关键的一个函数，主要作用就是计算了所有gas的使用，也包含了调用虚拟机
func (st *StateTransition) TransitionDb() (ret []byte, usedGas uint64, failed bool, err error) {
	// 检查nonce是否符合要求，检查账户是否足够支付gas（preCheck中调用的buyGas）
	if err = st.preCheck(); err != nil {
		return
	}
	// 2. 在state_processor.go中ApplyTransaction通过AsMessage方法，把transaction转换成了message
	// 具体为什么这么做的逻辑值得深入了解
	msg := st.msg
	// 发送方的地址
	sender := vm.AccountRef(msg.From())
	homestead := st.evm.ChainConfig().IsHomestead(st.evm.BlockNumber)

	// 3. 判断这笔交易是不是部署合约，如果to是nil，就是部署合约
	contractCreation := msg.To() == nil

	// Pay intrinsic gas
	// 计算固有成本的gas
	gas, err := IntrinsicGas(st.data, contractCreation, homestead)
	if err != nil {
		return nil, 0, false, err
	}
	// 调用useGas，用st.gas - IntrinsicGas
	if err = st.useGas(gas); err != nil {
		return nil, 0, false, err
	}

	var (
		evm = st.evm
		// vm errors do not effect consensus and are therefor
		// not assigned to err, except for insufficient balance
		// error.
		vmerr error
	)

	// 4. 判断是否创建合约，这部分代码就是开始调用虚拟机了
	if contractCreation {
		// evm.Create：进行创建合约的操作
		ret, _, st.gas, vmerr = evm.Create(sender, st.data, st.gas, st.value)
	} else {
		// Increment the nonce for the next transaction
		// 设置nonce？
		st.state.SetNonce(msg.From(), st.state.GetNonce(sender.Address())+1)
		ret, st.gas, vmerr = evm.Call(sender, st.to(), st.data, st.gas, st.value)
	}
	
	// 5. vmrr（虚拟机）发生错误
	if vmerr != nil {
		log.Debug("VM returned with error", "err", vmerr)
		// The only possible consensus-error would be if there wasn't
		// sufficient balance to make the transfer happen. The first
		// balance transfer may never fail.

		// 判断错误类型，如果是balance不足，直接停止，否则继续运行
		// 所以唯一的consensus-error就是余额不足？
		if vmerr == vm.ErrInsufficientBalance {
			return nil, 0, false, vmerr
		}
	}
	
	// 6. 退还多余的gas
	st.refundGas()
	// 7. st.evm.Coinbase是矿工账户？这里的逻辑貌似是把账户扣除的gas总额（eth 即手续费）转给矿工账户
	st.state.AddBalance(st.evm.Coinbase, new(big.Int).Mul(new(big.Int).SetUint64(st.gasUsed()), st.gasPrice))

	return ret, st.gasUsed(), vmerr != nil, err
}

// 1. 处理退还剩余gas的函数
func (st *StateTransition) refundGas() {
	// Apply refund counter, capped to half of the used gas.
	// 2. 退还上限是已用gas的一半
	refund := st.gasUsed() / 2
	// 如果退还数量超过一半，就递归调用，直到不超过一半

	// 3. state就是stateDB，直接访问状态树，在stateDB里面定义了很多方法，看懂这部分，就知道以太坊中总是提到的state是什么意思了
	// state（实际上的stateDB）接口在vm/interface.go中被定义
	if refund > st.state.GetRefund() {
		refund = st.state.GetRefund()
	}

	// 4. 当前剩余的gas + 退款的gas
	st.gas += refund

	// Return ETH for remaining gas, exchanged at the original rate.
	// （剩余gas+退回gas）* gasPrice，把剩下的eth退回给balance
	remaining := new(big.Int).Mul(new(big.Int).SetUint64(st.gas), st.gasPrice)
	st.state.AddBalance(st.msg.From(), remaining)

	// Also return remaining gas to the block gas counter so it is
	// available for the next transaction.

	// 5. 把block剩余的gas加回gas pool，这样可以用来处理下一笔交易，看得出来以太坊开发团队花了不少时间写这些小细节
	st.gp.AddGas(st.gas)
}

// gasUsed returns the amount of gas used up by the state transition.
// 1. 这里实际上就是计算出Gas used，在伦敦升级之前，消耗掉的gas实际上就是gas used * gas price
func (st *StateTransition) gasUsed() uint64 {
	// 2. gas used = st.initialGas（gas使用量） - st.gas（剩余的gas）
	return st.initialGas - st.gas
}
