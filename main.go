package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	"github.com/joho/godotenv"
	persistentcookiejar "github.com/juju/persistent-cookiejar"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/term"
)

type Friend struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	BioLinks    []string `json:"bioLinks"`
}

type User struct {
	ID                    string   `json:"id"`
	DisplayName           string   `json:"displayName"`
	RequiresTwoFactorAuth []string `json:"requiresTwoFactorAuth"`
}

func login(client *http.Client, username, password string) (*User, error) {
	req, err := http.NewRequest("GET", "https://api.vrchat.cloud/api/1/auth/user", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("User-Agent", "Friends/1.0.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var currentUser User
	if err := json.NewDecoder(resp.Body).Decode(&currentUser); err != nil {
		return nil, err
	}

	if len(currentUser.RequiresTwoFactorAuth) > 0 {
		if err := handleTwoFactorAuth(client, currentUser.RequiresTwoFactorAuth); err != nil {
			return nil, err
		}

		req, err = http.NewRequest("GET", "https://api.vrchat.cloud/api/1/auth/user", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Friends/1.0.0")

		resp, err = client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if err := json.NewDecoder(resp.Body).Decode(&currentUser); err != nil {
			return nil, err
		}
	}

	return &currentUser, nil
}

func handleTwoFactorAuth(client *http.Client, methods []string) error {
	isHandled := false
	for _, method := range methods {
		var codeType, url string
		switch method {
		case "emailOtp":
			codeType = "Email認証コード"
			url = "https://api.vrchat.cloud/api/1/auth/twofactorauth/emailotp/verify"
		case "totp", "otp":
			if isHandled {
				continue
			}
			codeType = "TOTP認証コード"
			url = "https://api.vrchat.cloud/api/1/auth/twofactorauth/totp/verify"
			isHandled = true
		default:
			return fmt.Errorf("未知の二要素認証方法: %s", method)
		}

		fmt.Printf("%sを入力してください: ", codeType)
		code, err := readSecretInput()
		if err != nil {
			return err
		}

		body := fmt.Sprintf(`{"code": "%s"}`, code)
		verifyReq, _ := http.NewRequest("POST", url, strings.NewReader(body))
		verifyReq.Header.Set("Content-Type", "application/json")
		verifyReq.Header.Set("User-Agent", "Friends/1.0.0")

		verifyResp, err := client.Do(verifyReq)
		if err != nil {
			return err
		}
		defer verifyResp.Body.Close()

		if verifyResp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(verifyResp.Body)
			return fmt.Errorf("2FA認証失敗 (%s): %s", method, string(bodyBytes))
		}

		fmt.Printf("%s認証成功\n", codeType)
	}
	return nil
}

func performLogin(client *http.Client, username, password string, jar *persistentcookiejar.Jar) error {
	user, err := login(client, username, password)
	if err != nil {
		return err
	}
	fmt.Printf("ログイン成功: %s\n", user.DisplayName)

	if err := jar.Save(); err != nil {
		return fmt.Errorf("クッキーの保存に失敗しました: %v", err)
	}
	return nil
}

func readSecretInput() (string, error) {
	byteCode, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}
	fmt.Println()
	return strings.TrimSpace(string(byteCode)), nil
}

func getAllFriends(client *http.Client) ([]Friend, error) {
	var allFriends []Friend

	onlineFriends, err := getFriendsList(client, false)
	if err != nil {
		return nil, err
	}
	allFriends = append(allFriends, onlineFriends...)

	offlineFriends, err := getFriendsList(client, true)
	if err != nil {
		return nil, err
	}
	allFriends = append(allFriends, offlineFriends...)

	return allFriends, nil
}

func getFriendsList(client *http.Client, offline bool) ([]Friend, error) {
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.vrchat.cloud/api/1/auth/user/friends?offline=%t", offline), nil)
	req.Header.Set("User-Agent", "Friends/1.0.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var friends []Friend
	if err := json.NewDecoder(resp.Body).Decode(&friends); err != nil {
		return nil, err
	}

	return friends, nil
}

func filterTwitterLinks(friends []Friend) map[string]string {
	twitterLinks := make(map[string]string)
	for _, friend := range friends {
		for _, link := range friend.BioLinks {
			if strings.Contains(link, "twitter.com") || strings.Contains(link, "x.com") {
				twitterLinks[friend.DisplayName] = link
				break
			}
		}
	}
	return twitterLinks
}

func displayTwitterLinks(twitterLinks map[string]string) {
	fmt.Println("以下のTwitterリンクが見つかりました:")
	fmt.Println()
	for displayName, link := range twitterLinks {
		fmt.Printf("%s: %s\n", displayName, link)
	}
	fmt.Println()
}

func confirmAndOpenLinks(twitterLinks map[string]string) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("これらのリンクをすべて開きますか？ [y/n]: ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if strings.ToLower(input) == "y" {
		for _, link := range twitterLinks {
			if err := openLinkInBrowser(link); err != nil {
				fmt.Printf("リンクを開く処理に失敗しました: %s\n", err)
			}
		}
	} else {
		fmt.Println("リンクを開く処理をスキップしました。")
	}
}

func openLinkInBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("未対応のプラットフォームです")
	}

	return cmd.Start()
}

func loadCache(filename string) (map[string]string, error) {
	cache := make(map[string]string)

	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return cache, nil
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}

	return cache, nil
}

func saveCache(filename string, cache map[string]string) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func filterNewLinks(twitterLinks map[string]string, cache map[string]string) map[string]string {
	newLinks := make(map[string]string)
	for displayName, link := range twitterLinks {
		if cachedLink, ok := cache[displayName]; !ok || cachedLink != link {
			newLinks[displayName] = link
		}
	}
	return newLinks
}

func getCurrentUser(client *http.Client) (*User, error) {
	req, err := http.NewRequest("GET", "https://api.vrchat.cloud/api/1/auth/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Friends/1.0.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("セッションが無効です")
	}

	var currentUser User
	if err := json.NewDecoder(resp.Body).Decode(&currentUser); err != nil {
		return nil, err
	}
	return &currentUser, nil
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}

	username := os.Getenv("VRCHAT_USERNAME")
	password := os.Getenv("VRCHAT_PASSWORD")

	if username == "" || password == "" {
		log.Fatal("環境変数 VRCHAT_USERNAME または VRCHAT_PASSWORD が設定されていません")
	}

	cookieFile := "cookies.json"

	jar, err := persistentcookiejar.New(&persistentcookiejar.Options{
		Filename:         cookieFile,
		PublicSuffixList: publicsuffix.List,
	})
	if err != nil {
		log.Fatal(err)
	}

	client := &http.Client{
		Jar: jar,
	}

	if user, err := getCurrentUser(client); err == nil {
		fmt.Printf("既存のセッションを使用します。ログインユーザー: %s\n", user.DisplayName)
	} else {
		if err := performLogin(client, username, password, jar); err != nil {
			log.Fatal(err)
		}
	}

	cache, err := loadCache("cache.json")
	if err != nil {
		log.Fatal(err)
	}

	friends, err := getAllFriends(client)
	if err != nil {
		log.Fatal(err)
	}

	twitterLinks := filterTwitterLinks(friends)
	friendCount := len(friends)
	twitterLinkCount := len(twitterLinks)

	if friendCount == 0 || twitterLinkCount == 0 {
		fmt.Println("フレンドが見つかりませんでした。")
		return
	}

	fmt.Printf("フレンド人数 %d 人のうち %d 人がリンクを登録していました。\n", friendCount, twitterLinkCount)

	newLinks := filterNewLinks(twitterLinks, cache)

	if len(newLinks) == 0 {
		fmt.Println("リンクを更新したフレンドはいませんでした。")
		return
	}

	displayTwitterLinks(newLinks)
	confirmAndOpenLinks(newLinks)

	for displayName, link := range newLinks {
		cache[displayName] = link
	}
	if err := saveCache("cache.json", cache); err != nil {
		log.Fatal(err)
	}
}
