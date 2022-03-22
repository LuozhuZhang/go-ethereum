// Copyright 2015 The go-ethereum Authors
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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// 负责过渡？什么意思
// StateProcessor is a basic Processor, which takes care of transitioning
// state from one point to another.
//
// StateProcessor implements Processor.
type StateProcessor struct {
	config *params.ChainConfig // Chain configuration options
	bc     *BlockChain         // Canonical block chain
	engine consensus.Engine    // Consensus engine used for block rewards
}

// NewStateProcessor initialises a new StateProcessor.
func NewStateProcessor(config *params.ChainConfig, bc *BlockChain, engine consensus.Engine) *StateProcessor {
	return &StateProcessor{
		config: config,
		bc:     bc,
		engine: engine,
	}
}

// 通过tx驱动StateProcessor的发生，从而改变Ethereum的state

// Process processes the state changes according to the Ethereum rules by running
// the transaction messages using the statedb and applying any rewards to both
// the processor (coinbase) and any included uncles.

// Process也会return处理tx过程中产生的log和receipt，还有消耗的gas
// 估计也包含了调用contract的数据，因为合约也是通过transaction来调用的

// Process returns the receipts and logs accumulated during the process and
// returns the amount of gas that was used in the process. If any of the
// transactions failed to execute due to insufficient gas it will return an error.

// 这个是EVM的入口函数
// 会遍历执行一个block里面的所有交易、stateDB是访问世界状态的接口

// 如何维持以太坊的状态？需要每个block拿到上一个block的世界树之后执行所有交易，并且得出相同的结果
func (p *StateProcessor) Process(block *types.Block, statedb *state.StateDB, cfg vm.Config) (types.Receipts, []*types.Log, uint64, error) {
	var (
		receipts types.Receipts
		usedGas  = new(uint64)
		header   = block.Header()
		allLogs  []*types.Log
		// gaspool：当前block的gaslimit上限，所在block最多能容纳的gas大小（即block.GasLimit）
		gp       = new(GasPool).AddGas(block.GasLimit())
	)
	// Mutate the block and state according to any hard-fork specs
	// hard-fork相关的一些逻辑
	if p.config.DAOForkSupport && p.config.DAOForkBlock != nil && p.config.DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}
	// Iterate over and process the individual transactions
	// 这里的逻辑大概就是遍历这个block里的所有交易，然后一一执行
	for i, tx := range block.Transactions() {
		statedb.Prepare(tx.Hash(), block.Hash(), i)
		// EVM的入口？
  	// 并且会为交易（transaction）生成收据（receipt）
  	// 传入GasPool（指针？），遍历每一步交易都会从gaspool减去这笔交易的gas，如果gaspool减到<0（gaspool里没有gas了），之后所有交易都会失败
		receipt, _, err := ApplyTransaction(p.config, p.bc, nil, gp, statedb, header, tx, usedGas, cfg)
		if err != nil {
			// 任何一个交易执行失败，该状态函数会直接返回err
			return nil, nil, 0, err
		}
		receipts = append(receipts, receipt)
		allLogs = append(allLogs, receipt.Logs...)
	}
	// Finalize the block, applying any consensus engine specific extras (e.g. block rewards)
	// consensus.Engine引擎，可能有一些reward的功能
	p.engine.Finalize(p.bc, header, statedb, block.Transactions(), block.Uncles(), receipts)

	return receipts, allLogs, *usedGas, nil
}

// ApplyTransaction attempts to apply a transaction to the given state database
// and uses the input parameters for its environment. It returns the receipt
// for the transaction, gas used and an error if the transaction failed,
// indicating the block was invalid.

// ApplyTransaction将transaction输入到stateDB中
// 交易成功后return receipt、gas、error等信息
func ApplyTransaction(config *params.ChainConfig, bc ChainContext, author *common.Address, gp *GasPool, statedb *state.StateDB, header *types.Header, tx *types.Transaction, usedGas *uint64, cfg vm.Config) (*types.Receipt, uint64, error) {
	// 将transaction转成message，说实话我没太搞懂，这个数据转换是什么意思？后面研究一下
	// 现在搞懂了，是关于数据结构的转换，所有转换在core/type文件夹之下，此处在core/type/transaction中定义
	// message.data = tx.txdata.payload，交易中的input（合约代码）？
	sg, err := tx.AsMessage(types.MakeSigner(config, header.Number))
	if err != nil {
		return nil, 0, err
	}
	// Create a new context to be used in the EVM environment
	// 创建EVMContext，貌似是用于EVM的环境
	// 在newEVM的同时貌似也会new一个EVM的解释器（EVM interpreter）
	// transaction其实就是通过EVM解释器 -> interpreter -> run函数执行，详细见文档的结构图
	context := NewEVMContext(msg, header, bc, author)

	// Create a new environment which holds all relevant information
	// about the transaction and calling mechanisms.

	/* 
	让EVM处理交易：
	EVM -> EVM interpreter -> run函数处理交易，就在这里发生的
	多行注释：option+shift+A
	Params
	@ vmenv：虚拟机实例（值得深入研究一下NewEVM）
	@ gp：gaspool

	return：
	@ gas：交易结束使用了多少gas
	*/

	/* 
	更为详细的代码在core/vm/evm.go中 ->
	evm.interpreter = NewEVMInterpreter(evm, config) 创建了一个EVM解释器
	*/
	vmenv := vm.NewEVM(context, statedb, config, cfg)
	// Apply the transaction to the current state (included in the env)

	/* 		
	详细代码在 core/state_transaction.go中，
	return NewStateTransition(evm, msg, gp).TransitionDb()：

	在里面生成并return了一个 NewStateTransition 对象，把所有虚拟机执行需要的数据都传入 StateTransition
	然后调用transactionDB方法交给虚拟机执行
	*/

	// 这里的gs还是整个block的gas limit，就是循环减gas的逻辑
	_, gas, failed, err := ApplyMessage(vmenv, msg, gp)
	if err != nil {
		return nil, 0, err
	}
	// Update the state with pending changes
	var root []byte
	if config.IsByzantium(header.Number) {
		statedb.Finalise(true)
	} else {
		// 根据EIP158修改状态？
		root = statedb.IntermediateRoot(config.IsEIP158(header.Number)).Bytes()
	}
	*usedGas += gas

	// Create a new receipt for the transaction, storing the intermediate root and gas used by the tx
	// based on the eip phase, we're passing whether the root touch-delete accounts.

	// 为每一笔交易创建一个receipt，传入root、failed、usedgas等信息
	receipt := types.NewReceipt(root, failed, *usedGas)
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = gas
	// if the transaction created a contract, store the creation address in the receipt.
	// transaction中如果to是null，说明部署了一个新合约，把这个新合约的地址放到receipt.ContractAddress里
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(vmenv.Context.Origin, tx.Nonce())
	}
	// Set the receipt logs and create a bloom for filtering
	receipt.Logs = statedb.GetLogs(tx.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})

	return receipt, gas, err
}
