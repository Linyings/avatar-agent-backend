//go:build ignore

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type adminSetup struct {
	db *gorm.DB
	in *bufio.Reader
}

type options struct {
	list        bool
	create      bool
	promoteID   string
	revokeID    string
	username    string
	email       string
	password    string
	displayName string
	yes         bool
}

func main() {
	opts := parseFlags()

	cfg := config.Load()
	db, err := database.Open(cfg)
	if err != nil {
		exitf("数据库连接失败: %v", err)
	}
	if cfg.AutoMigrate {
		if err := database.AutoMigrate(db); err != nil {
			exitf("数据库初始化失败: %v", err)
		}
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close()
	}

	setup := adminSetup{db: db, in: bufio.NewReader(os.Stdin)}
	if hasAction(opts) {
		if err := setup.runAction(opts); err != nil {
			exitf("%v", err)
		}
		return
	}

	if err := setup.runInteractive(); err != nil {
		exitf("%v", err)
	}
}

func parseFlags() options {
	opts := options{}
	flag.BoolVar(&opts.list, "list", false, "列出所有用户")
	flag.BoolVar(&opts.create, "create", false, "创建新的管理员用户")
	flag.StringVar(&opts.promoteID, "promote", "", "将指定用户提升为管理员，支持完整 ID 或 ID 前缀")
	flag.StringVar(&opts.revokeID, "revoke", "", "撤销指定用户的管理员权限，支持完整 ID 或 ID 前缀")
	flag.StringVar(&opts.username, "username", "", "新管理员用户名")
	flag.StringVar(&opts.email, "email", "", "新管理员邮箱")
	flag.StringVar(&opts.password, "password", "", "新管理员密码，也可用 ADMIN_PASSWORD 环境变量")
	flag.StringVar(&opts.displayName, "display-name", "", "新管理员显示名称，默认使用用户名")
	flag.BoolVar(&opts.yes, "yes", false, "跳过提升或撤销管理员权限时的确认")
	flag.Parse()
	return opts
}

func hasAction(opts options) bool {
	return opts.list || opts.create || opts.promoteID != "" || opts.revokeID != ""
}

func (s adminSetup) runAction(opts options) error {
	switch {
	case opts.list:
		_, err := s.listUsers()
		return err
	case opts.create:
		return s.createAdminUser(opts)
	case opts.promoteID != "":
		return s.promoteUser(opts.promoteID, opts.yes)
	case opts.revokeID != "":
		return s.revokeAdmin(opts.revokeID, opts.yes)
	default:
		return nil
	}
}

func (s adminSetup) runInteractive() error {
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("          管理员用户设置工具")
	fmt.Println(strings.Repeat("=", 60))

	for {
		fmt.Println("\n请选择操作：")
		fmt.Println("1. 列出所有用户")
		fmt.Println("2. 创建新管理员用户")
		fmt.Println("3. 将现有用户提升为管理员")
		fmt.Println("4. 撤销用户的管理员权限")
		fmt.Println("5. 退出")

		choice, err := s.prompt("\n请输入选项 (1-5): ")
		if err != nil {
			return err
		}

		switch choice {
		case "1":
			if _, err := s.listUsers(); err != nil {
				return err
			}
		case "2":
			if err := s.createAdminUser(options{}); err != nil {
				fmt.Printf("错误: %v\n", err)
			}
		case "3":
			users, err := s.listUsers()
			if err != nil {
				return err
			}
			if len(users) == 0 {
				continue
			}
			userID, err := s.prompt("\n请输入要提升的用户ID (前8位即可): ")
			if err != nil {
				return err
			}
			if err := s.promoteUser(userID, false); err != nil {
				fmt.Printf("错误: %v\n", err)
			}
		case "4":
			users, err := s.listUsers()
			if err != nil {
				return err
			}
			if len(users) == 0 {
				continue
			}
			userID, err := s.prompt("\n请输入要撤销管理员权限的用户ID (前8位即可): ")
			if err != nil {
				return err
			}
			if err := s.revokeAdmin(userID, false); err != nil {
				fmt.Printf("错误: %v\n", err)
			}
		case "5":
			fmt.Println("\n再见！")
			return nil
		default:
			fmt.Println("无效的选项，请重新选择")
		}
	}
}

func (s adminSetup) listUsers() ([]models.User, error) {
	var users []models.User
	if err := s.db.Order("created_at ASC").Find(&users).Error; err != nil {
		return nil, err
	}
	if len(users) == 0 {
		fmt.Println("没有找到任何用户")
		return users, nil
	}

	fmt.Println("\n现有用户列表：")
	fmt.Println(strings.Repeat("-", 90))
	fmt.Printf("%-8s %-20s %-30s %-10s %-10s %-10s\n", "ID", "用户名", "邮箱", "管理员", "激活", "已验证")
	fmt.Println(strings.Repeat("-", 90))
	for _, user := range users {
		fmt.Printf("%-8s %-20s %-30s %-10s %-10s %-10s\n",
			user.ID.String()[:8],
			user.Username,
			user.Email,
			yesNo(user.IsAdmin),
			yesNo(user.IsActive),
			yesNo(user.IsVerified),
		)
	}
	return users, nil
}

func (s adminSetup) createAdminUser(opts options) error {
	fmt.Println("\n=== 创建新管理员用户 ===")

	username := strings.TrimSpace(opts.username)
	if username == "" {
		value, err := s.prompt("请输入用户名: ")
		if err != nil {
			return err
		}
		username = value
	}
	if username == "" {
		return errors.New("用户名不能为空")
	}

	email := strings.TrimSpace(opts.email)
	if email == "" {
		value, err := s.prompt("请输入邮箱: ")
		if err != nil {
			return err
		}
		email = value
	}
	if email == "" {
		return errors.New("邮箱不能为空")
	}

	password := opts.password
	if password == "" {
		password = os.Getenv("ADMIN_PASSWORD")
	}
	if password == "" {
		value, err := s.promptPassword("请输入密码: ")
		if err != nil {
			return err
		}
		password = value
	}
	if len(password) < 8 {
		return errors.New("密码长度不能少于 8 位")
	}

	displayName := strings.TrimSpace(opts.displayName)
	if displayName == "" && opts.username == "" && opts.email == "" {
		value, err := s.prompt("请输入显示名称 (可选): ")
		if err != nil {
			return err
		}
		displayName = value
	}
	if displayName == "" {
		displayName = username
	}

	now := time.Now().UTC()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("密码加密失败: %w", err)
	}

	var created models.User
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureUniqueUser(tx, username, email); err != nil {
			return err
		}

		created = models.User{
			Username:        username,
			Email:           email,
			DisplayName:     &displayName,
			PasswordHash:    string(hash),
			IsActive:        true,
			IsVerified:      true,
			IsAdmin:         true,
			EmailVerifiedAt: &now,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		if err := createDefaultProfile(tx, created.ID); err != nil {
			return err
		}
		return createDefaultTokenAccount(tx, created.ID)
	})
	if err != nil {
		return err
	}

	fmt.Println("\n成功创建管理员用户！")
	fmt.Printf("用户ID: %s\n", created.ID.String())
	fmt.Printf("用户名: %s\n", created.Username)
	fmt.Printf("邮箱: %s\n", created.Email)
	fmt.Printf("显示名称: %s\n", displayName)
	return nil
}

func (s adminSetup) promoteUser(idPrefix string, yes bool) error {
	user, err := s.findUserByIDPrefix(idPrefix)
	if err != nil {
		return err
	}
	if user.IsAdmin {
		fmt.Printf("用户 '%s' 已经是管理员了\n", user.Username)
		return nil
	}
	if !yes {
		ok, err := s.confirm(fmt.Sprintf("确认将用户 '%s' (%s) 提升为管理员？ [y/N]: ", user.Username, user.Email))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("操作已取消")
			return nil
		}
	}

	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Update("is_admin", true).Error; err != nil {
		return err
	}
	fmt.Printf("\n成功将用户 '%s' 提升为管理员！\n", user.Username)
	return nil
}

func (s adminSetup) revokeAdmin(idPrefix string, yes bool) error {
	user, err := s.findUserByIDPrefix(idPrefix)
	if err != nil {
		return err
	}
	if !user.IsAdmin {
		fmt.Printf("用户 '%s' 不是管理员\n", user.Username)
		return nil
	}

	var adminCount int64
	if err := s.db.Model(&models.User{}).Where("is_admin = ?", true).Count(&adminCount).Error; err != nil {
		return err
	}
	if adminCount <= 1 {
		return errors.New("不能撤销最后一个管理员的权限")
	}

	if !yes {
		ok, err := s.confirm(fmt.Sprintf("确认撤销用户 '%s' (%s) 的管理员权限？ [y/N]: ", user.Username, user.Email))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("操作已取消")
			return nil
		}
	}

	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Update("is_admin", false).Error; err != nil {
		return err
	}
	fmt.Printf("\n成功撤销用户 '%s' 的管理员权限！\n", user.Username)
	return nil
}

func (s adminSetup) findUserByIDPrefix(prefix string) (models.User, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return models.User{}, errors.New("用户 ID 不能为空")
	}

	if id, err := uuid.Parse(prefix); err == nil {
		var user models.User
		if err := s.db.First(&user, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return models.User{}, fmt.Errorf("未找到用户 ID %s", prefix)
			}
			return models.User{}, err
		}
		return user, nil
	}

	var users []models.User
	if err := s.db.Order("created_at ASC").Find(&users).Error; err != nil {
		return models.User{}, err
	}
	var matches []models.User
	for _, user := range users {
		if strings.HasPrefix(user.ID.String(), prefix) {
			matches = append(matches, user)
		}
	}
	switch len(matches) {
	case 0:
		return models.User{}, fmt.Errorf("未找到匹配的用户 ID %s", prefix)
	case 1:
		return matches[0], nil
	default:
		return models.User{}, fmt.Errorf("用户 ID 前缀 %s 匹配到多个用户，请输入更长的 ID", prefix)
	}
}

func ensureUniqueUser(tx *gorm.DB, username, email string) error {
	var count int64
	if err := tx.Model(&models.User{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("用户名 '%s' 已存在", username)
	}
	if err := tx.Model(&models.User{}).Where("email = ?", email).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("邮箱 '%s' 已存在", email)
	}
	return nil
}

func createDefaultProfile(tx *gorm.DB, userID uuid.UUID) error {
	profile := models.UserProfile{
		UserID:               userID,
		Timezone:             "Asia/Shanghai",
		Language:             "zh-CN",
		Preferences:          datatypes.JSON([]byte(`{"theme":"light","auto_save":true}`)),
		NotificationSettings: datatypes.JSON([]byte(`{"email_notifications":true}`)),
	}
	return tx.Create(&profile).Error
}

func createDefaultTokenAccount(tx *gorm.DB, userID uuid.UUID) error {
	account := models.UserTokenAccount{
		UserID: userID,
	}
	return tx.Create(&account).Error
}

func (s adminSetup) prompt(label string) (string, error) {
	fmt.Print(label)
	value, err := s.in.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func (s adminSetup) promptPassword(label string) (string, error) {
	fmt.Print(label)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		bytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(bytes)), nil
	}
	return s.prompt("")
}

func (s adminSetup) confirm(label string) (bool, error) {
	answer, err := s.prompt(label)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "错误: "+format+"\n", args...)
	os.Exit(1)
}
