package core

import (
    "context"
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "github.com/go-git/go-git/v5/plumbing/object"
    "github.com/workerindex/gdir/dist"
    "io/fs"
    "io/ioutil"
    "log"
    "os"
    "path/filepath"
    "regexp"
    "strconv"
    "strings"
    "syscall"
    "time"

    "github.com/cloudflare/cloudflare-go"
    "github.com/go-git/go-git/v5"
    "github.com/go-git/go-git/v5/config"
    "github.com/google/go-github/v31/github"

    "golang.org/x/crypto/ssh/terminal"
    "golang.org/x/oauth2"
)

func LoadConfigFile() (err error) {
    if Config.ConfigFile == "" {
        return
    }

    stat, err := os.Stat(Config.ConfigFile)
    if os.IsNotExist(err) {
        err = nil
        return
    }
    if err != nil {
        return
    }

    if stat.IsDir() {
        err = fmt.Errorf("config file cannot be a directory: %s", Config.ConfigFile)
        return
    }

    fmt.Printf("Loading existing config from %s\n", Config.ConfigFile)

    b, err := ioutil.ReadFile(Config.ConfigFile)
    if err != nil {
        return
    }

    clone, err := json.Marshal(&Config)
    if err != nil {
        return
    }

    if err = json.Unmarshal(b, &Config); err != nil {
        return
    }

    // command line options overwrite config file options
    if err = json.Unmarshal(clone, &Config); err != nil {
        return
    }

    return
}

func ValidateConfig() bool {
    return Config.CloudflareEmail != "" &&
        Config.CloudflareKey != "" &&
        Config.CloudflareAccount != "" &&
        Config.CloudflareWorker != "" &&
        Config.GistToken != "" &&
        Config.GistUser != "" &&
        Config.GistID.Accounts != "" &&
        Config.GistID.Static != "" &&
        Config.GistID.Users != "" &&
        Config.SecretKey != "" &&
        Config.AccountRotation != 0 &&
        Config.AccountCandidates != 0 &&
        Config.AccountsJSONDir != "" &&
        Config.AccountsCount != 0
}

func SetupProxy() (err error) {
    if Config.Proxy != "" {
        fmt.Println("Your network proxy:", Config.Proxy)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.Proxy = ""
        }
    }
    if Config.Proxy == "" {
        fmt.Printf("Setup network proxy for gdir.\n")
        fmt.Printf("Example: socks5://127.0.0.1:1080\n")
        fmt.Printf("Press enter to skip proxy and use direct connect.\n")
        fmt.Printf("Proxy address: ")
        fmt.Scanln(&Config.Proxy)
        fmt.Println("")
        err = SaveConfigFile()
    }
    if err = os.Setenv("ALL_PROXY", Config.Proxy); err != nil {
        return err
    }
    return
}

func EnterCloudflareEmail() (err error) {
    if Config.CloudflareEmail != "" {
        fmt.Println("Your Cloudflare login Email:", Config.CloudflareEmail)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.CloudflareEmail = ""
        }
    }
    if Config.CloudflareEmail == "" {
        for loop := true; loop; loop = Config.CloudflareEmail == "" {
            fmt.Printf("Your Cloudflare login Email: ")
            fmt.Scanln(&Config.CloudflareEmail)
        }
        fmt.Println("")
        err = SaveConfigFile()
    }
    return
}

func EnterCloudflareKey() (err error) {
    if Config.CloudflareKey != "" {
        fmt.Println("Your Cloudflare API Key:", Config.CloudflareKey)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.CloudflareKey = ""
        }
    }
    if Config.CloudflareKey == "" {
        for loop := true; loop; loop = Config.CloudflareKey == "" {
            fmt.Println("Please visit https://dash.cloudflare.com/profile/api-tokens and get")
            fmt.Printf("your Global API Key: ")
            fmt.Scanln(&Config.CloudflareKey)
        }
        fmt.Println("")
        err = SaveConfigFile()
    }
    return
}

func InitCloudflareAPI() (err error) {
    if Cf != nil {
        return
    }
    Cf, err = cloudflare.New(Config.CloudflareKey, Config.CloudflareEmail)
    return
}

func SelectCloudflareAccount() (err error) {
    if Cf.AccountID != "" {
        return
    }
    if Config.CloudflareAccount != "" {
        fmt.Println("Your selected Cloudflare account:", Config.CloudflareAccount)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.CloudflareAccount = ""
        }
    }
    if Config.CloudflareAccount == "" {
        var line string
        var selection uint64
        var accounts []cloudflare.Account
        if accounts, _, err = Cf.Accounts(cloudflare.PaginationOptions{}); err != nil {
            return
        }
        if len(accounts) == 0 {
            err = fmt.Errorf("no accounts under your cloudflare")
            return
        }
        if len(accounts) == 1 {
            Config.CloudflareAccount = accounts[0].ID
        } else {
            fmt.Println("Your available Cloudflare accounts:")
            for i, account := range accounts {
                fmt.Printf("    (%d) %s [%s]\n", i+1, account.Name, account.ID)
            }
            for {
                fmt.Printf("Choose an account: ")
                fmt.Scanln(&line)
                if selection, err = strconv.ParseUint(line, 10, 64); err != nil {
                    continue
                }
                Config.CloudflareAccount = accounts[selection-1].ID
                break
            }
        }
        if err = SaveConfigFile(); err != nil {
            return
        }
    }
    Cf.AccountID = Config.CloudflareAccount
    return
}

func SetupCloudflareSubdomain() (err error) {
    var line string
    if Config.CloudflareSubdomain != "" {
        return
    }
    subdomain, err := Cf.GetSubdomain()
    if err != nil {
        return
    }
    if subdomain == "" {
        fmt.Printf("You don't have a Cloudflare subdomain yet. It's a free service provided\n")
        fmt.Printf("by Cloudflare to host your workers. We are going to register one for you.\n")
        for {
            fmt.Printf("Please enter a name for your subdomain:")
            fmt.Scanln(&line)
            if !regexp.MustCompile(`^\s*[a-zA-Z0-9\-_]+\s*$`).MatchString(line) {
                fmt.Printf("Invalid subdomain format!\n")
                continue
            }
            if err = Cf.RegisterSubdomain(strings.TrimSpace(line)); err != nil {
                fmt.Printf("Cannot register this subdomain. Please try another one.\n")
                err = nil
                continue
            }
            break
        }
    }
    Config.CloudflareSubdomain = subdomain
    fmt.Printf("Your Cloudflare subdomain is: %s.workers.dev\n", subdomain)
    return
}

func SelectWorker() (err error) {
    if Config.CloudflareWorker != "" {
        fmt.Println("Your selected Cloudflare Worker ID:", Config.CloudflareWorker)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.CloudflareWorker = ""
        }
    }
    if Config.CloudflareWorker == "" {
        var line string
        var selection uint64
        var resp cloudflare.WorkerListResponse
        if resp, err = Cf.ListWorkerScripts(); err != nil {
            return
        }
        if len(resp.WorkerList) == 0 {
            err = EnterNewWorkerName()
        } else {
            fmt.Println("Your available Cloudflare Workers:")
            for i, worker := range resp.WorkerList {
                fmt.Printf("    (%d) %s\n", i+1, worker.ID)
            }
            fmt.Println("    (0) Create a new Worker     (Default)")
            for {
                fmt.Printf("Choose one of above: ")
                fmt.Scanln(&line)
                if line == "" {
                    err = EnterNewWorkerName()
                    break
                }
                if selection, err = strconv.ParseUint(line, 10, 64); err != nil {
                    continue
                }
                if selection == 0 {
                    err = EnterNewWorkerName()
                    break
                } else {
                    Config.CloudflareWorker = resp.WorkerList[selection-1].ID
                    break
                }
            }
            if err = SaveConfigFile(); err != nil {
                return
            }
        }
    }
    return
}

func EnterNewWorkerName() (err error) {
    var line string

    fmt.Println("Naming rule:")

    fmt.Println("    start with a letter")
    var rule1 = regexp.MustCompile(`^[[:alpha:]]`)

    fmt.Println("    end with a letter or digit")
    var rule2 = regexp.MustCompile(`\w$`)

    fmt.Println("    include only letters, digits, underscore, and hyphen")
    var rule3 = regexp.MustCompile(`^[\w_-]+$`)

    fmt.Println("    be 63 characters or less")

    for {
        fmt.Printf("Please enter a name for your new Worker: ")
        fmt.Scanln(&line)
        line = strings.TrimSpace(line)
        if len(line) <= 63 && rule1.MatchString(line) && rule2.MatchString(line) && rule3.MatchString(line) {
            break
        }
    }
    Config.CloudflareWorker = line
    return SaveConfigFile()
}

func EnterGistToken() (err error) {
    if Config.GistToken != "" {
        fmt.Println("Your GitHub Gist Token:", Config.GistToken)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.GistToken = ""
        }
    }
    if Config.GistToken == "" {
        for loop := true; loop; loop = Config.GistToken == "" {
            fmt.Println("Please visit https://github.com/settings/tokens and generate a new")
            fmt.Printf("token with \"gist\" scope: ")
            fmt.Scanln(&Config.GistToken)
        }
        fmt.Println("")
        err = SaveConfigFile()
    }
    return
}

func InitGitHubAPI() (err error) {
    if Gh != nil {
        return
    }
    Gh = github.NewClient(
        oauth2.NewClient(
            context.Background(),
            oauth2.StaticTokenSource(
                &oauth2.Token{AccessToken: Config.GistToken},
            )))
    return
}

func ConfigureGist(name string, gistID *string, username, token string) (err error) {
    if *gistID != "" {
        fmt.Printf("Your %s Gist: %s\n", name, *gistID)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            *gistID = ""
        }
    }
    if *gistID == "" {
        fmt.Printf("Specify how you want to configure your %s Gist:\n", name)
        fmt.Println("    (1) Create a new Gist                   (default)")
        fmt.Println("    (2) Enter an existing Gist URL / ID")
        for {
            var line string
            fmt.Printf("Please enter your choice: ")
            fmt.Scanln(&line)
            if line == "" || line == "1" {
                err = CreateNewGist(name, gistID)
                break
            } else if line == "2" {
                err = EnterGistID(name, gistID)
                break
            }
        }
    }
    return ConfigureGistGit(strings.ToLower(name), *gistID, username, token)
}

func CreateNewGist(name string, gistID *string) (err error) {
    name = fmt.Sprintf(".gdir-%s", strings.ToLower(name))
    gist, _, err := Gh.Gists.Create(context.Background(), &github.Gist{
        Description: &name,
        Files: map[github.GistFilename]github.GistFile{
            github.GistFilename(name): {
                Content: &name,
            },
        },
    })
    if err != nil {
        return
    }
    if Config.Debug {
        b, _ := json.MarshalIndent(gist, "", "    ")
        log.Printf("Created new Gist for %s:\n", name, string(b))
    }
    *gistID = *gist.ID
    return SaveConfigFile()
}

func EnterGistID(name string, gistID *string) (err error) {
    var line string
    for {
        fmt.Printf("Please enter a Gist URL / ID for %s: ", name)
        fmt.Scanln(&line)
        if line, err = ParseGistID(line); err != nil {
            continue
        }
        *gistID = line
        break
    }
    return SaveConfigFile()
}

func GetGistUser() (err error) {
    var user *github.User
    if user, _, err = Gh.Users.Get(context.Background(), ""); err != nil {
        return
    }
    Config.GistUser = *user.Login
    return SaveConfigFile()
}

func ConfigureGistGit(dir, gistID, username, token string) (err error) {
    var r *git.Repository
    var remote *git.Remote
    var rConfig *config.Config

    giturl := fmt.Sprintf("https://%s:%s@gist.github.com/%s.git", username, token, gistID)
    if r, err = git.PlainOpen(dir); err == git.ErrRepositoryNotExists {
        if _, err = os.Stat(dir); os.IsNotExist(err) {
            log.Printf("Cloning %s to %s ...", giturl, dir)
            if r, err = git.PlainClone(dir, false, &git.CloneOptions{
                URL:      giturl,
                Progress: os.Stdout,
            }); err != nil {
                return fmt.Errorf("failed to clone repo %s: %w", dir, err)
            }
        } else {
            log.Printf("Init git at %s ...", dir)
            if r, err = git.PlainInit(dir, false); err != nil {
                return fmt.Errorf("failed to init repo %s: %w", dir, err)
            }
        }
    } else if err != nil {
        return fmt.Errorf("failed to open repo %s: %w", dir, err)
    }

    remote, err = r.Remote("origin")
    if err == git.ErrRemoteNotFound {
        remote, err = r.CreateRemote(&config.RemoteConfig{
            Name: "origin",
            URLs: []string{giturl},
        })
    } else if err != nil {
        return fmt.Errorf("failed to get remote info of repo %s: %w", dir, err)
    }

    if rConfig, err = r.Config(); err != nil {
        return fmt.Errorf("failed to read git config of repo %s: %w", dir, err)
    }

    rConfigChanged := false

    gitUser := "gdir"
    gitEmail := "gdir@gmail.com"

    if rConfig.User.Name != gitUser {
        rConfig.User.Name = gitUser
        rConfigChanged = true
    }

    if rConfig.User.Email != gitEmail {
        rConfig.User.Email = gitEmail
        rConfigChanged = true
    }

    remoteConfig := remote.Config()
    if len(remoteConfig.URLs) == 0 || remoteConfig.URLs[0] != giturl {
        remoteConfig.URLs = []string{giturl}
        rConfig.Remotes[remoteConfig.Name] = remoteConfig
        rConfigChanged = true
    }

    if rConfigChanged {
        if err = r.SetConfig(rConfig); err != nil {
            return fmt.Errorf("failed to set git config of repo %s: %w", dir, err)
        }
    }

    if _, err = r.Branch("master"); err != nil {
        if err != git.ErrBranchNotFound {
            return fmt.Errorf("failed to get branch master of repo %s: %w", dir, err)
        }
        if err = r.CreateBranch(&config.Branch{
            Name:   "master",
            Remote: "origin",
            Merge:  "refs/heads/master",
        }); err != nil {
            return fmt.Errorf("failed to create master branch in repo %s: %w", dir, err)
        }
    }

    return
}

func ConfigureSecretKey() (err error) {
    if Config.SecretKey != "" {
        fmt.Println("Your gdir secret key:", Config.SecretKey)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.SecretKey = ""
        }
    }
    if Config.SecretKey == "" {
        fmt.Println("Specify how you want to configure your gdir secret key:")
        fmt.Println("    (1) Generate secure random value           (default)")
        fmt.Println("    (2) Enter your own secret key      (not recommended)")
        for {
            var line string
            fmt.Printf("Please enter your choice: ")
            fmt.Scanln(&line)
            if line == "" || line == "1" {
                err = GenerateSecretKey()
                break
            } else if line == "2" {
                err = EnterSecretKey()
                break
            }
        }
    }
    return
}

func GenerateSecretKey() (err error) {
    b := make([]byte, 64)
    if _, err = rand.Read(b); err != nil {
        return
    }
    Config.SecretKey = hex.EncodeToString(b)
    if Config.Debug {
        log.Printf("Generated secret key: %s", Config.SecretKey)
    }
    return SaveConfigFile()
}

func EnterSecretKey() (err error) {
    var line string
    fmt.Printf("Please enter your secure gdir master secret key: ")
    fmt.Scanln(&line)
    Config.SecretKey = strings.TrimSpace(line)
    return SaveConfigFile()
}

func ConfigureAccountRotation() (err error) {
    var line string
    if Config.AccountRotationStr != "" {
        Config.AccountRotation = 0
        line = Config.AccountRotationStr
    } else if Config.AccountRotation > 0 {
        fmt.Println("Account candidates rotations interval:", Config.AccountRotation)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.AccountRotation = 0
        }
    }
    if Config.AccountRotation == 0 {
        for {
            if line == "" {
                fmt.Printf("Please enter account candidates rotations interval (default 60): ")
                fmt.Scanln(&line)
            }
            line = strings.TrimSpace(line)
            if line == "" {
                Config.AccountRotation = 60
                break
            }
            if Config.AccountRotation, err = strconv.ParseUint(line, 10, 64); err == nil {
                break
            }
            line = ""
        }
        err = SaveConfigFile()
    }
    return
}

func ConfigureAccountCandidates() (err error) {
    var line string
    if Config.AccountCandidatesStr != "" {
        Config.AccountCandidates = 0
        line = Config.AccountCandidatesStr
    } else if Config.AccountCandidates > 0 {
        fmt.Println("Account candidates size:", Config.AccountCandidates)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.AccountCandidates = 0
        }
    }
    if Config.AccountCandidates == 0 {
        for {
            if line == "" {
                fmt.Printf("Please enter account candidates size (default 10): ")
                fmt.Scanln(&line)
            }
            line = strings.TrimSpace(line)
            if line == "" {
                Config.AccountCandidates = 10
                break
            }
            if Config.AccountCandidates, err = strconv.ParseUint(line, 10, 64); err == nil {
                break
            }
            line = ""
        }
        err = SaveConfigFile()
    }
    return
}

func EnterAccountsJSONDir() (err error) {
    if Config.AccountsJSONDir != "" {
        fmt.Println("Your Accounts JSON directory:", Config.AccountsJSONDir)
        if !PromptYesNoWithDefault("Is it correct?", true) {
            Config.AccountsJSONDir = ""
        }
    }
    if Config.AccountsJSONDir == "" {
        for loop := true; loop; loop = Config.AccountsJSONDir == "" {
            fmt.Println("Please follow https://github.com/xyou365/AutoRclone to generate")
            fmt.Printf("Accounts JSON directory: ")
            fmt.Scanln(&Config.AccountsJSONDir)
        }
        fmt.Println("")
        err = SaveConfigFile()
    }
    return
}

func ProcessAccountsJSONDir() (err error) {
    if Config.AccountsCount > 0 {
        if PromptYesNoWithDefault(fmt.Sprintf("You have added %d accounts, do you want to re-scan for new accounts?", Config.AccountsCount), false) {
            Config.AccountsCount = 0
        }
    }
    if Config.AccountsCount == 0 {
        var files []os.FileInfo
        var inPath string
        var inBytes []byte
        var outBytes []byte
        if files, err = ioutil.ReadDir(Config.AccountsJSONDir); err != nil {
            return
        }

        if _, err = os.Stat("accounts"); os.IsNotExist(err) {
            if err = os.MkdirAll("accounts", 0700); err != nil {
                return
            }
        }

        for i, file := range files {
            if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") && file.Size() > 0 {
                inPath = filepath.Join(Config.AccountsJSONDir, file.Name())
                fmt.Printf("Encrypting account %d: %s\n", i, file.Name())

                if inBytes, err = ioutil.ReadFile(inPath); err != nil {
                    return
                }

                if outBytes, err = GCMEncrypt(Config.SecretKey, "account", inBytes); err != nil {
                    return
                }

                if err = ioutil.WriteFile(filepath.Join("accounts", fmt.Sprintf("%d", i)), outBytes, 0600); err != nil {
                    return
                }
                Config.AccountsCount++
            }
        }
        err = SaveConfigFile()
    }
    return
}

func ConfigureAdminUser() (err error) {
    var user User
    var files []os.FileInfo
    var bytePassword []byte
    if _, err = os.Stat("users"); !os.IsNotExist(err) {
        if files, err = ioutil.ReadDir("users"); err != nil {
            return
        }
        for _, file := range files {
            if !file.IsDir() {
                return
            }
        }
    } else {
        if err = os.MkdirAll("users", 0700); err != nil {
            return
        }
    }
    fmt.Println("Add an admin user...")
    for loop := true; loop; loop = user.Name == "" {
        fmt.Printf("Please enter your admin user name: ")
        fmt.Scanln(&user.Name)
    }
    for loop := true; loop; loop = user.Pass == "" {
        fmt.Printf("Please enter your admin user password: ")
        if bytePassword, err = terminal.ReadPassword(int(syscall.Stdin)); err != nil {
            if _, err = fmt.Scanln(&user.Pass); err != nil {
                return
            }
        } else {
            fmt.Println()
            user.Pass = string(bytePassword)
        }
    }
    return SaveUser(&user)
}

func ComputeUserPath(name string) (userPath string, err error) {
    hash := sha256.New()
    hash.Write([]byte(Config.SecretKey))
    hash.Write([]byte(name))
    userPath = filepath.Join("users", hex.EncodeToString(hash.Sum(nil)))
    return
}

func ConfigureUserAccess(user *User) (err error) {
    for {
        confirmed := false
        if len(user.DrivesAllowList) > 0 {
            if confirmed, err = ConfigureUserAccessList("allow-list", &user.DrivesAllowList, "block-list", &user.DrivesBlockList); err != nil {
                return
            }
        } else if len(user.DrivesBlockList) > 0 {
            if confirmed, err = ConfigureUserAccessList("block-list", &user.DrivesBlockList, "allow-list", &user.DrivesAllowList); err != nil {
                return
            }
        } else {
            var line string
            var drives []string
            fmt.Println("The user currently has global access to all drives.")
            fmt.Println("Please specify what do you want to do with it:")
            fmt.Println("    (1) Confirm                         (default)")
            fmt.Println("    (2) Convert to allow-list access control list")
            fmt.Println("    (3) Convert to block-list access control list")
            fmt.Printf("Please enter your choice: ")
            fmt.Scanln(&line)
            if line == "1" || line == "" {
                confirmed = true
            } else if line == "2" {
                fmt.Println("(Use comma to separate between drive IDs.)")
                fmt.Printf("Enter allow-list access control list of drives: ")
                fmt.Scanln(&line)
                drives = strings.Split(line, ",")
                for i, drive := range drives {
                    drives[i] = strings.TrimSpace(drive)
                }
                user.DrivesAllowList = drives
            } else if line == "3" {
                fmt.Println("(Use comma to separate between drive IDs.)")
                fmt.Printf("Enter block-list access control list of drives: ")
                fmt.Scanln(&line)
                drives = strings.Split(line, ",")
                for i, drive := range drives {
                    drives[i] = strings.TrimSpace(drive)
                }
                user.DrivesBlockList = drives
            }
        }
        if confirmed {
            break
        }
    }
    return
}

func ConfigureUserAccessList(targetListName string, targetList *[]string, counterListName string, counterList *[]string) (confirmed bool, err error) {
    var line string
    var drives []string
    *counterList = nil
    fmt.Printf("The user currently has following drives in its %s access list:\n", targetListName)
    for i, drive := range *targetList {
        fmt.Printf("    (%d) %s\n", i+1, drive)
    }
    fmt.Println("Please specify what do you want to do with it:")
    fmt.Println("    (1) Confirm                         (default)")
    fmt.Println("    (2) Append drives to the list")
    fmt.Println("    (3) Remove drives from the list")
    fmt.Println("    (4) Replace with a new list of drives")
    fmt.Printf("    (5) Convert to %s access control\n", counterListName)
    fmt.Println("    (6) Disable access control on the user")
    fmt.Printf("Please enter your choice: ")
    fmt.Scanln(&line)
    if line == "1" || line == "" {
        confirmed = true
    } else if line == "2" {
        fmt.Println("(Use comma to separate between drive IDs.)")
        fmt.Printf("Append drives to %s access list: ", targetListName)
        fmt.Scanln(&line)
        drives = strings.Split(line, ",")
        for _, drive := range drives {
            found := false
            drive = strings.TrimSpace(drive)
            for _, d := range *targetList {
                if d == drive {
                    found = true
                    break
                }
            }
            if !found {
                *targetList = append(*targetList, drive)
            }
        }
    } else if line == "3" {
        idxs := []uint64{}
        drives = []string{}
        for {
            fmt.Println("(Enter numbers from the list above)")
            fmt.Println("(Use comma to separate between selections)")
            fmt.Printf("Remove drives from %s access list: ", targetListName)
            fmt.Scanln(&line)
            valid := true
            for _, idx := range strings.Split(line, ",") {
                var i uint64
                if i, err = strconv.ParseUint(idx, 10, 64); err != nil {
                    err = nil
                    valid = false
                }
                idxs = append(idxs, i)
            }
            if valid {
                break
            }
        }
        for i, drive := range *targetList {
            found := false
            for _, idx := range idxs {
                if uint64(i+1) == idx {
                    found = true
                    break
                }
            }
            if !found {
                drives = append(drives, drive)
            }
        }
        *targetList = drives
    } else if line == "4" {
        fmt.Println("(Use comma to separate between drive IDs.)")
        fmt.Printf("New %s access list of drives: ", targetListName)
        fmt.Scanln(&line)
        drives = strings.Split(line, ",")
        for i, drive := range drives {
            drives[i] = strings.TrimSpace(drive)
        }
        *targetList = drives
    } else if line == "5" {
        fmt.Printf("Converting from %s access list into %s access list...\n", targetListName, counterListName)
        *counterList = *targetList
        *targetList = nil
    } else if line == "6" {
        fmt.Println("Removing access control list from the user...")
        *counterList = nil
        *targetList = nil
    }
    return
}

func SaveUser(user *User) (err error) {
    var b []byte
    var userPath string
    if userPath, err = ComputeUserPath(user.Name); err != nil {
        return
    }
    fmt.Printf("Saving user to %s ...\n", userPath)
    if b, err = json.Marshal(&user); err != nil {
        return
    }
    if b, err = GCMEncrypt(Config.SecretKey, "user", b); err != nil {
        return
    }
    return ioutil.WriteFile(userPath, b, 0600)
}

func DeployGist(dir string) (err error) {
    var r *git.Repository
    var w *git.Worktree
    var status git.Status

    if r, err = git.PlainOpen(dir); err != nil {
        return fmt.Errorf("failed to open git repo at %s: %w", dir, err)
    }

    if w, err = r.Worktree(); err != nil {
        return fmt.Errorf("failed to get git worktree of repo %s: %w", dir, err)
    }

    if status, err = w.Status(); err != nil {
        return fmt.Errorf("failed to get worktree status of repo %s: %w", dir, err)
    }

    if status.IsClean() {
        return
    }

    for p, s := range status {
        if s.Worktree == git.Deleted {
            if _, err = w.Remove(p); err != nil {
                return
            }
        }
    }

    if err = w.AddGlob("."); err != nil {
        return fmt.Errorf("failed to stage changes to repo %s: %w", dir, err)
    }
    if _, err = w.Commit("[gdir] deploy", &git.CommitOptions{
        Author: &object.Signature{
            Name:  "gdir",
            Email: "gdir@mail.com",
            When:  time.Now(),
        },
    }); err != nil {
        return fmt.Errorf("failed to commit repo %s: %w", dir, err)
    }

    fmt.Printf("Deploying %s to Gist...\n", dir)
    if err = r.Push(&git.PushOptions{
        Force:    true,
        Progress: os.Stdout,
    }); err != nil {
        return fmt.Errorf("failed to push to repo %s: %w", dir, err)
    }
    return
}

func CopyStaticFiles() (err error) {
    if err = os.MkdirAll("static", 0700); err != nil {
        return
    }
    files, err := ioutil.ReadDir("static")
    if err != nil {
        return
    }
    for _, file := range files {
        if file.Name() == ".git" {
            continue
        }
        if err = os.RemoveAll(filepath.Join("static", file.Name())); err != nil {
            return
        }
    }
    if err = fs.WalkDir(dist.StaticFs, "static", func(path string, d fs.DirEntry, err error) error {
        if err != nil {
            return err
        }
        if d.IsDir() {
            if err = os.MkdirAll(path, 0700); err != nil {
                return err
            }
        } else {
            bytes, err := dist.StaticFs.ReadFile(path)
            if err != nil {
                return err
            }
            if err = ioutil.WriteFile(path, bytes, d.Type()); err != nil {
                return err
            }
        }
        return err
    }); err != nil {
        return
    }
    return
}

func DeployWorker() (err error) {
    b, err := dist.StaticFs.ReadFile("worker.js")
    if err != nil {
        return
    }
    r := strings.NewReplacer(
        "__SECRET__", Config.SecretKey,
        "__ACCOUNTS_COUNT__", strconv.FormatUint(Config.AccountsCount, 10),
        "__ACCOUNT_ROTATION__", strconv.FormatUint(Config.AccountRotation, 10),
        "__ACCOUNT_CANDIDATES__", strconv.FormatUint(Config.AccountCandidates, 10),
        "__USERS_URL__", fmt.Sprintf("https://gist.githubusercontent.com/%s/%s/raw/", Config.GistUser, Config.GistID.Users),
        "__STATIC_URL__", fmt.Sprintf("https://gist.githubusercontent.com/%s/%s/raw/", Config.GistUser, Config.GistID.Static),
        "__ACCOUNTS_URL__", fmt.Sprintf("https://gist.githubusercontent.com/%s/%s/raw/", Config.GistUser, Config.GistID.Accounts),
    )
    script := r.Replace(string(b))
    fmt.Printf("Deploying Cloudflare Worker %s...\n", Config.CloudflareWorker)
    if _, err = Cf.UploadWorker(&cloudflare.WorkerRequestParams{
        ScriptName: Config.CloudflareWorker,
    }, script); err != nil {
        return
    }
    if err = Cf.PublishWorker(Config.CloudflareWorker); err != nil {
        return
    }
    fmt.Printf("\nYour gdir is now live at https://%s.%s.workers.dev\n", Config.CloudflareWorker, Config.CloudflareSubdomain)
    fmt.Println("Check here to create custom routes with your own domain names:\nhttps://developers.cloudflare.com/workers/about/routes/")
    fmt.Println("\nPlease backup config.json for this setup.")
    return
}

func SaveConfigFile() (err error) {
    b, err := json.MarshalIndent(&Config, "", "    ")
    if err != nil {
        return
    }
    return ioutil.WriteFile(Config.ConfigFile, b, 0600)
}

func EnterUsername(name *string) (err error) {
    for *name == "" {
        fmt.Printf("Username: ")
        fmt.Scanln(name)
    }
    return
}

func ReadUserByPath(userPath string, user *User) (err error) {
    var inBytes []byte
    if _, e := os.Stat(userPath); e != nil {
        if os.IsNotExist(e) {
            return ErrUserNotExists
        }
        return e
    }
    if inBytes, err = ioutil.ReadFile(userPath); err != nil {
        return
    }
    if inBytes, err = GCMDecrypt(Config.SecretKey, "user", inBytes); err != nil {
        return
    }
    if err = json.Unmarshal(inBytes, user); err != nil {
        return
    }
    return
}

func ReadUser(name string, user *User) (err error) {
    var userPath string
    if userPath, err = ComputeUserPath(name); err != nil {
        return
    }
    return ReadUserByPath(userPath, user)
}

func LoadOldNewUsers(name string, oldUser, newUser *User) (err error) {
    if err = ReadUser(name, oldUser); err != nil {
        if err == ErrUserNotExists {
            fmt.Printf("Creating new user %s ...\n", name)
            return nil
        }
        return
    }
    fmt.Printf("Editing existing user %s ...\n", name)
    newUser.DrivesAllowList = oldUser.DrivesAllowList
    newUser.DrivesBlockList = oldUser.DrivesBlockList
    return
}

func EnterUserPassword(oldUser, newUser *User) (err error) {
    var bytePassword []byte
    if oldUser.Pass != "" && newUser.Pass == "" {
        fmt.Println("Password:", oldUser.Pass)
        if PromptYesNoWithDefault("Is it correct?", true) {
            newUser.Pass = oldUser.Pass
        }
    }
    if newUser.Pass == "" {
        for loop := true; loop; loop = newUser.Pass == "" {
            fmt.Printf("Password: ")
            if bytePassword, err = terminal.ReadPassword(int(syscall.Stdin)); err != nil {
                if _, err = fmt.Scanln(&newUser.Pass); err != nil {
                    return
                }
            } else {
                fmt.Println()
                newUser.Pass = string(bytePassword)
            }
        }
    }
    return
}

func ListUsers() (err error) {
    var fis []os.FileInfo
    if fis, err = ioutil.ReadDir("users"); err != nil {
        return err
    }
    index := 0
    for _, info := range fis {
        var user User
        if info.IsDir() || strings.HasPrefix(info.Name(), ".") {
            continue
        }
        if err = ReadUserByPath(filepath.Join("users", info.Name()), &user); err != nil {
            return err
        }
        index = index + 1
        fmt.Printf("User #%d:\n", index)
        fmt.Printf("      Username: %s\n", user.Name)
        fmt.Printf("      Password: %s\n", user.Pass)
        if len(user.DrivesBlockList) == 0 && len(user.DrivesAllowList) == 0 {
            fmt.Printf("    Permission: Full-Access (Admin)\n")
        } else if len(user.DrivesBlockList) > 0 {
            fmt.Printf("    Permission: Block-List\n")
            fmt.Printf("    Block-List: %s\n", strings.Join(user.DrivesBlockList, ","))
        } else if len(user.DrivesAllowList) > 0 {
            fmt.Printf("    Permission: Allow-List\n")
            fmt.Printf("    Allow-List: %s\n", strings.Join(user.DrivesAllowList, ","))
        }
        fmt.Printf("      Filename: %s\n", info.Name())
        fmt.Println()
    }
    return
}

func RemoveUser() (err error) {
    var name string
    var userPath string
    if err = EnterUsername(&name); err != nil {
        return
    }
    if userPath, err = ComputeUserPath(name); err != nil {
        return
    }
    if _, e := os.Stat(userPath); os.IsNotExist(e) {
        fmt.Printf("User %s does not exist!\n", name)
        return
    }
    return os.Remove(userPath)
}
