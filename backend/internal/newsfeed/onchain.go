package newsfeed

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync/atomic"
)

// 鏈上監控(以太坊):用 Alchemy / Infura 免費額度的 HTTPS JSON-RPC 輪詢 ERC-20 Transfer 事件。
// 只用 eth_blockNumber / eth_getLogs / eth_call 三個方法,免費額度很夠用。

// transferTopic = keccak256("Transfer(address,address,uint256)")
const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// ChainLog 是一筆 ERC-20 轉帳。
type ChainLog struct {
	Token  string // 代幣合約地址(小寫)
	From   string
	To     string
	Amount *big.Int // 原始數量(未除 decimals)
	Tx     string
	Block  uint64
}

var rpcID atomic.Int64

func (c *Client) rpc(endpoint, method string, params []any, out any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpcID.Add(1), "method": method, "params": params})
	b, err := c.do("POST", endpoint, string(body), nil)
	if err != nil {
		return err
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return fmt.Errorf("解析失敗: %v", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("rpc %s: %s", method, resp.Error.Message)
	}
	return json.Unmarshal(resp.Result, out)
}

// BlockNumber 回最新區塊高度。
func (c *Client) BlockNumber(endpoint string) (uint64, error) {
	var hexs string
	if err := c.rpc(endpoint, "eth_blockNumber", []any{}, &hexs); err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimPrefix(hexs, "0x"), 16, 64)
}

func padAddr(a string) string {
	return "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(strings.ToLower(a), "0x")
}

// TransferLogs 抓 [from, to] 區塊內、轉入(intoWallets=true)或轉出這些錢包的 ERC-20 轉帳。
func (c *Client) TransferLogs(endpoint string, from, to uint64, wallets []string, intoWallets bool) ([]ChainLog, error) {
	padded := make([]any, len(wallets))
	for i, w := range wallets {
		padded[i] = padAddr(w)
	}
	topics := []any{transferTopic, nil, padded} // topic2 = to
	if !intoWallets {
		topics = []any{transferTopic, padded} // topic1 = from
	}
	filter := map[string]any{"fromBlock": fmt.Sprintf("0x%x", from), "toBlock": fmt.Sprintf("0x%x", to), "topics": topics}
	var logs []struct {
		Address     string   `json:"address"`
		Topics      []string `json:"topics"`
		Data        string   `json:"data"`
		TxHash      string   `json:"transactionHash"`
		BlockNumber string   `json:"blockNumber"`
	}
	if err := c.rpc(endpoint, "eth_getLogs", []any{filter}, &logs); err != nil {
		return nil, err
	}
	var out []ChainLog
	for _, l := range logs {
		if len(l.Topics) < 3 { // ERC-721 等格式不同的跳過
			continue
		}
		amt, ok := new(big.Int).SetString(strings.TrimPrefix(l.Data, "0x"), 16)
		if !ok {
			continue
		}
		blk, _ := strconv.ParseUint(strings.TrimPrefix(l.BlockNumber, "0x"), 16, 64)
		out = append(out, ChainLog{Token: strings.ToLower(l.Address), From: topicAddr(l.Topics[1]), To: topicAddr(l.Topics[2]),
			Amount: amt, Tx: l.TxHash, Block: blk})
	}
	return out, nil
}

func topicAddr(t string) string {
	t = strings.TrimPrefix(strings.ToLower(t), "0x")
	if len(t) < 40 {
		return "0x" + t
	}
	return "0x" + t[len(t)-40:]
}

// TokenMeta 用 eth_call 讀代幣的 symbol() 與 decimals()。
func (c *Client) TokenMeta(endpoint, token string) (symbol string, decimals int, err error) {
	call := func(selector string) (string, error) {
		var res string
		err := c.rpc(endpoint, "eth_call", []any{map[string]string{"to": token, "data": selector}, "latest"}, &res)
		return res, err
	}
	dec, err := call("0x313ce567")
	if err != nil {
		return "", 0, err
	}
	if d, ok := new(big.Int).SetString(strings.TrimPrefix(dec, "0x"), 16); ok {
		decimals = int(d.Int64())
	}
	sym, err := call("0x95d89b41")
	if err != nil {
		return "", decimals, err
	}
	return decodeABIString(sym), decimals, nil
}

// decodeABIString 解 ABI 編碼的 string 回傳值(也容忍舊代幣回傳 bytes32 的情況)。
func decodeABIString(h string) string {
	b, err := hex.DecodeString(strings.TrimPrefix(h, "0x"))
	if err != nil || len(b) == 0 {
		return ""
	}
	if len(b) >= 64 {
		n := new(big.Int).SetBytes(b[32:64]).Int64()
		if n > 0 && 64+n <= int64(len(b)) {
			return strings.ToUpper(strings.TrimSpace(string(b[64 : 64+n])))
		}
	}
	return strings.ToUpper(strings.Trim(string(b[:min(32, len(b))]), "\x00 ")) // bytes32
}

// TokenAmount 把原始數量依 decimals 換成浮點數。
func TokenAmount(raw *big.Int, decimals int) float64 {
	f, _ := new(big.Float).SetInt(raw).Float64()
	return f / math.Pow10(decimals)
}
