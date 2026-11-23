/**
 * Generated from internal/server/httpapi/openapi.json by
 * internal/server/httpapi/tsgen. Do not edit by hand; run
 * `go generate ./internal/server/httpapi` instead.
 */

export interface components {
    schemas: {
        AccountBot: {
            /** Format: date-time */
            created_at: string;
            /** Format: int64 */
            id: number;
            tokens: components["schemas"]["AccountToken"][];
            username: string;
        };
        AccountInvite: {
            /** Format: date-time */
            CreatedAt: string;
            CreatedBy: string;
            ExpiresAt: string | null;
            /** Format: int64 */
            ID: number;
            Note: string;
            RedeemedAt: string | null;
            RedeemedBy: string | null;
        };
        AccountProject: {
            /** Format: int64 */
            id: number;
            name: string;
            published: boolean;
            remote_key: string;
        };
        AccountResponse: {
            bots: components["schemas"]["AccountBot"][];
            connections: components["schemas"]["OAuthGrant"][];
            invites: components["schemas"]["AccountInvite"][];
            projects: components["schemas"]["AccountProject"][];
            reparse: components["schemas"]["Status"];
            tokens: components["schemas"]["AccountToken"][];
            user: components["schemas"]["Viewer"];
        };
        AccountToken: {
            /** Format: date-time */
            CreatedAt: string;
            /** Format: int64 */
            ID: number;
            LastUsedAt: string | null;
            Name: string;
            RevokedAt: string | null;
            Scope: string;
        };
        Analytics: {
            Agents: components["schemas"]["Breakdown"][];
            Cache: components["schemas"]["CacheStats"];
            Models: components["schemas"]["Breakdown"][];
            Series: components["schemas"]["DayPoint"][];
            /** Format: int64 */
            Sessions: number;
            /** Format: int64 */
            TotalCacheRead: number;
            /** Format: int64 */
            TotalCacheWrite: number;
            TotalCost: number;
            /** Format: int64 */
            TotalIn: number;
            /** Format: int64 */
            TotalOut: number;
            /** Format: int64 */
            TotalReasoning: number;
            Users: components["schemas"]["Breakdown"][];
        };
        AnnounceSession: {
            agent: "claude" | "codex" | "pi" | "cursor" | "grok" | "opencode";
            cwd?: string;
            git_branch?: string;
            /** @default "remote" */
            kind: "remote" | "standalone" | "orphaned";
            local_root?: string;
            machine?: string;
            project_remote?: string;
            source_session_id: string;
            /** @default false */
            terminal: boolean;
        };
        AttachmentView: {
            /** Format: int64 */
            ByteLen: number;
            Filename: string;
            MediaType: string;
            /** Format: int64 */
            MessageOrdinal: number;
            SHA256: string;
        };
        Breakdown: {
            /** Format: int64 */
            CacheRead: number;
            /** Format: int64 */
            CacheWrite: number;
            CostKnown: boolean;
            CostUSD: number;
            /** Format: int64 */
            Input: number;
            Label: string;
            /** Format: int64 */
            Output: number;
            /** Format: int64 */
            Reasoning: number;
            /** Format: int64 */
            Sessions: number;
        };
        CacheStats: {
            /** Format: int64 */
            CacheRead: number;
            /** Format: int64 */
            CacheWrite: number;
            /** Format: int64 */
            Input: number;
            /** Format: int64 */
            Output: number;
            SavingsUSD: number;
        };
        ChurnFile: {
            /** Format: int64 */
            Edits: number;
            Path: string;
            Project: string;
            /** Format: int64 */
            ProjectID: number;
            /** Format: int64 */
            Sessions: number;
        };
        ChurnNode: {
            /** Format: int64 */
            Edits: number;
            Folder: string;
            Path: string;
            Project: string;
            /** Format: int64 */
            Sessions: number;
        };
        ChurnTrend: {
            /** Format: int64 */
            Clipped: number;
            Files: number[];
            /** Format: int64 */
            Projects: number;
            ReEdits: number[];
            /** Format: int64 */
            TotalHotFiles: number;
            /** Format: int64 */
            TotalReEdits: number;
            Tree: components["schemas"]["ChurnNode"][];
        };
        ConcurrencyStats: {
            AvgConcurrent: number;
            BusiestUser: string;
            /** Format: int64 */
            BusiestUserPeak: number;
            /** Format: int64 */
            FleetPeak: number;
            /** Format: date-time */
            FleetPeakAt: string;
            /** Format: int64 */
            Sessions: number;
        };
        ContextBucket: {
            /** Format: int64 */
            Count: number;
            /** Format: int64 */
            Hi: number;
            /** Format: int64 */
            Lo: number;
        };
        ContextHealthStats: {
            /** Format: int64 */
            PeakTokensMax: number;
            /** Format: int64 */
            PeakTokensP50: number;
            /** Format: int64 */
            PeakTokensP90: number;
            /** Format: int64 */
            Sessions: number;
            /** Format: int64 */
            SessionsWithReset: number;
            /** Format: int64 */
            TotalResets: number;
        };
        ContextMarker: {
            Kind: string;
            /** Format: int64 */
            Tokens: number;
        };
        CreateBot: {
            username: string;
        };
        CreateInvite: {
            expires_hours?: number;
            note?: string;
        };
        CreateToken: {
            name: string;
            /** @default "ingest" */
            scope: "ingest" | "read" | "full";
        };
        CreatedInviteResponse: {
            expires_at: string | null;
            /** Format: int64 */
            id: number;
            invite_token: string;
            note: string;
        };
        CreatedTokenResponse: {
            /** Format: int64 */
            id: number;
            name: string;
            scope: string;
            token: string;
        };
        DateRange: {
            /** Format: int64 */
            Days: number;
            Key: string;
            Label: string;
        };
        DayPoint: {
            /** Format: int64 */
            CacheRead: number;
            /** Format: int64 */
            CacheWrite: number;
            CostUSD: number;
            /** Format: date-time */
            Day: string;
            /** Format: int64 */
            Input: number;
            /** Format: int64 */
            Output: number;
        };
        DeletedBotResponse: {
            deleted: boolean;
        };
        DeletedSessionResponse: {
            deleted: boolean;
            /** Format: int64 */
            project_id: number;
        };
        Economics: {
            AbandonedSharePct: number;
            CacheHitRate: number[];
            CacheHitRateLatest: number;
            CacheMeasured: boolean[];
            CacheSavings: number[];
            CostAbandoned: number[];
            CostCompleted: number[];
            CostOther: number[];
            TotalAbandoned: number;
            TotalCacheSavings: number;
            TotalSpend: number;
        };
        Error: {
            code?: string;
            error: string;
        };
        FacetCount: {
            /** Format: int64 */
            Count: number;
            Value: string;
        };
        FacetValues: {
            Agents: string[];
            Machines: string[];
            Users: string[];
        };
        FileChurn: {
            /** Format: int64 */
            Clipped: number;
            Files: components["schemas"]["ChurnFile"][];
        };
        FleetMix: {
            Models: components["schemas"]["ModelSeries"][];
            /** Format: int64 */
            NewestFirst: number;
            NewestModel: string;
        };
        Gallery: {
            LongestCostUSD: number;
            LongestDurationS: number;
            MedianCompletedCostUSD: number;
            MedianCostUSD: number;
            MedianDurationS: number;
            PriciestCostUSD: number;
            PriciestDurationS: number;
            Rows: components["schemas"]["GallerySession"][];
            /** Format: int64 */
            Total: number;
        };
        GallerySession: {
            Archetype: string;
            CostUSD: number;
            DurationS: number;
            Grade: string;
            Outcome: string;
        };
        GlobalFacetValues: {
            Agents: components["schemas"]["FacetCount"][];
            Machines: components["schemas"]["FacetCount"][];
            Projects: components["schemas"]["ProjectFacet"][];
            Users: components["schemas"]["FacetCount"][];
        };
        Insights: {
            Archetypes: components["schemas"]["LabeledCount"][];
            Churn: components["schemas"]["FileChurn"];
            Concurrency: components["schemas"]["ConcurrencyStats"];
            Context: components["schemas"]["ContextHealthStats"];
            Hygiene: components["schemas"]["PromptHygiene"];
            Quality: components["schemas"]["QualityDistribution"];
            Tools: components["schemas"]["ToolStats"];
            Trends: components["schemas"]["Trends"] | null;
            Velocity: components["schemas"]["VelocityStats"];
        };
        InsightsResponse: {
            /** Format: date-time */
            generated_at: string;
            insights: components["schemas"]["Insights"];
            range: string;
            ranges: components["schemas"]["DateRange"][];
        };
        LabeledCount: {
            /** Format: int64 */
            Count: number;
            Key: string;
        };
        Login: {
            /** Format: password */
            password: string;
            username: string;
        };
        LoginResponse: {
            is_admin: boolean;
            username: string;
        };
        Message: {
            Content: string;
            DuplicatePrompt: boolean;
            HasThinking: boolean;
            HasToolUse: boolean;
            Model: string;
            /** Format: int64 */
            Ordinal: number;
            /** Format: int64 */
            PromptDigest: number;
            PromptFactsCurrent: boolean;
            PromptNoCode: boolean;
            PromptShort: boolean;
            Role: string;
            /** Format: int64 */
            ThinkingBytes: number;
            ThinkingText: string;
            Timestamp: string | null;
            Usage: components["schemas"]["TurnUsage"] | null;
        };
        ModelCost: {
            CostUSD: number;
            Model: string;
            /** Format: int64 */
            Sessions: number;
            /** Format: int64 */
            Tokens: number;
        };
        ModelFallback: {
            DeclinedCacheRead: number | null;
            DeclinedCacheWrite: number | null;
            DeclinedInput: number | null;
            DeclinedOutput: number | null;
            DedupKey: string;
            FromModel: string;
            MessageOrdinal: number | null;
            OccurredAt: string | null;
            RefusalCategory: string;
            RefusalExplanation: string;
            ToModel: string;
            Trigger: string;
        };
        ModelSeries: {
            /** Format: int64 */
            First: number;
            Model: string;
            Share: number[];
            WindowShare: number;
        };
        OAuthConsentResponse: {
            app_csrf: string;
            client_id: string;
            client_name: string;
            code_challenge: string;
            csrf: string;
            redirect_uri: string;
            resource: string;
            state: string;
            username: string;
        };
        OAuthGrant: {
            ClientID: string;
            ClientName: string;
            /** Format: date-time */
            ConnectedAt: string;
            /** Format: date-time */
            LastUsedAt: string;
            Scope: string;
        };
        OverviewResponse: {
            analytics: components["schemas"]["Analytics"];
            range: string;
            ranges: components["schemas"]["DateRange"][];
            selected_user_ids: number[];
            users: components["schemas"]["OverviewUser"][];
        };
        OverviewUser: {
            /** Format: int64 */
            ID: number;
            IsAdmin: boolean;
            Username: string;
        };
        ProjectFacet: {
            /** Format: int64 */
            Count: number;
            /** Format: int64 */
            ID: number;
            Key: string;
            Kind: string;
            Name: string;
        };
        ProjectResponse: {
            analytics: components["schemas"]["Analytics"];
            facets: components["schemas"]["FacetValues"];
            filter: components["schemas"]["SessionFilter"];
            insights: components["schemas"]["Insights"];
            project: components["schemas"]["ProjectSummary"];
            range: string;
            ranges: components["schemas"]["DateRange"][];
            remainder: components["schemas"]["SessionRemainder"];
            sessions: components["schemas"]["ProjectSessionSummary"][];
        };
        ProjectSessionSummary: {
            Agent: string;
            EndedAt: string | null;
            GitBranch: string;
            Grade: string | null;
            /** Format: int64 */
            ID: number;
            LastActiveAt: string | null;
            Machine: string;
            /** Format: int64 */
            MessageCount: number;
            /** Format: int64 */
            ModelFallbackCount: number;
            Outcome: string;
            PublicID: string | null;
            StartedAt: string | null;
            Title: string;
            /** Format: int64 */
            TotalCacheRead: number;
            /** Format: int64 */
            TotalCacheWrite: number;
            TotalCostUSD: number;
            /** Format: int64 */
            TotalInput: number;
            /** Format: int64 */
            TotalOutput: number;
            /** Format: int64 */
            UserMessageCount: number;
            Username: string;
            Visibility: string;
        };
        ProjectSummary: {
            DisplayName: string;
            Host: string;
            /** Format: int64 */
            ID: number;
            Kind: string;
            LastActivity: string | null;
            OverviewPublic: boolean;
            Owner: string;
            RemoteKey: string;
            Repo: string;
            /** Format: int64 */
            SessionCount: number;
            /** Format: int64 */
            TotalCacheRead: number;
            /** Format: int64 */
            TotalCacheWrite: number;
            TotalCostUSD: number;
            /** Format: int64 */
            TotalInput: number;
            /** Format: int64 */
            TotalOutput: number;
        };
        ProjectionRebuild: {
            code: "projection_rebuild";
            error: "projection rebuild in progress";
            reparse: components["schemas"]["ReparseStatusResponse"];
        };
        ProjectsResponse: {
            projects: components["schemas"]["ProjectSummary"][];
            sparklines: {
                [key: string]: number[];
            };
        };
        PromptHygiene: {
            /** Format: int64 */
            Duplicate: number;
            /** Format: int64 */
            NoCodeContext: number;
            /** Format: int64 */
            Prompts: number;
            /** Format: int64 */
            Sessions: number;
            /** Format: int64 */
            Short: number;
            /** Format: int64 */
            UnstructuredStarts: number;
        };
        ProviderUsageBatch: {
            account_id: string;
            events: components["schemas"]["ProviderUsageEvent"][];
            provider: "cursor";
        };
        ProviderUsageEvent: {
            cache_read_tokens?: number;
            cache_write_tokens?: number;
            conversation_id?: string;
            /** @default false */
            cost_known: boolean;
            cost_usd?: number;
            event_key: string;
            input_tokens?: number;
            model?: string;
            /** Format: date-time */
            occurred_at: string;
            output_tokens?: number;
        };
        PublicOverviewResponse: {
            analytics: components["schemas"]["Analytics"];
            range: string;
            ranges: components["schemas"]["DateRange"][];
            username: string;
        };
        PublicProjectResponse: {
            analytics: components["schemas"]["Analytics"];
            insights: components["schemas"]["Insights"];
            project: components["schemas"]["ProjectSummary"];
            range: string;
            ranges: components["schemas"]["DateRange"][];
        };
        PublicSessionResponse: {
            snapshot: components["schemas"]["PublicSessionSnapshot"];
        };
        PublicSessionSnapshot: {
            Audit: components["schemas"]["SessionAudit"];
            Page: components["schemas"]["TranscriptPage"];
            /** Format: int64 */
            ProjectionRevision: number;
        };
        Publication: {
            published: boolean;
        };
        PublicationResponse: {
            published: boolean;
        };
        QualityDistribution: {
            /** Format: int64 */
            Graded: number;
            Grades: components["schemas"]["LabeledCount"][];
            Outcomes: components["schemas"]["LabeledCount"][];
            /** Format: int64 */
            Sessions: number;
        };
        Register: {
            invite_token?: string;
            /** Format: password */
            password: string;
            username: string;
        };
        RegisteredUserResponse: {
            /** Format: int64 */
            id: number;
            is_admin: boolean;
            username: string;
        };
        ReparseStatusResponse: {
            /** Format: int64 */
            done: number;
            /** Format: int64 */
            failed: number;
            in_progress: boolean;
            /** Format: date-time */
            started_at?: string;
            /** Format: int64 */
            total: number;
        };
        RevokedResponse: {
            revoked: boolean;
        };
        RhythmGrid: {
            Cells: number[][];
        };
        SearchSnippet: {
            /** Format: int64 */
            MatchEnd: number;
            /** Format: int64 */
            MatchStart: number;
            Text: string;
        };
        SessionAudit: {
            Detail: components["schemas"]["SessionDetail"];
            Fallbacks: components["schemas"]["ModelFallback"][];
            Signals: components["schemas"]["SessionSignals"];
            Subagents: components["schemas"]["SubagentRow"][];
        };
        SessionDetail: {
            Agent: string;
            Cwd: string;
            EndedAt: string | null;
            GitBranch: string;
            /** Format: int64 */
            ID: number;
            LastActiveAt: string | null;
            Machine: string;
            /** Format: int64 */
            MessageCount: number;
            /** Format: int64 */
            ModelFallbackCount: number;
            /** Format: int64 */
            OwnerID: number;
            /** Format: int64 */
            PRNumber: number;
            PRRepo: string;
            PRURL: string;
            ParentID: number | null;
            PermissionMode: string;
            /** Format: int64 */
            ProjectID: number;
            ProjectKey: string;
            ProjectKind: string;
            ProjectName: string;
            PublicID: string | null;
            ReasoningEffort: string;
            Slug: string;
            StartedAt: string | null;
            SubagentName: string;
            Title: string;
            /** Format: int64 */
            TotalCacheRead: number;
            TotalCacheSavingsUSD: number;
            /** Format: int64 */
            TotalCacheWrite: number;
            TotalCostUSD: number;
            /** Format: int64 */
            TotalInput: number;
            /** Format: int64 */
            TotalOutput: number;
            /** Format: int64 */
            UserMessageCount: number;
            Username: string;
            Visibility: string;
        };
        SessionEvent: {
            Attrs: {
                [key: string]: unknown;
            };
            Kind: string;
            MessageOrdinal: number | null;
            /** Format: date-time */
            OccurredAt: string;
        };
        SessionFilter: {
            /** Format: int64 */
            After: number;
            AfterVal: string;
            Agent: string;
            Desc: boolean;
            Grade: string;
            IncludeEmpty: boolean;
            IncludeSubagents: boolean;
            /** Format: int64 */
            Limit: number;
            Machine: string;
            /** Format: int64 */
            Offset: number;
            Outcome: string;
            /** Format: int64 */
            ProjectID: number;
            Query: string;
            Range: string;
            RequireSpan: boolean;
            /** Format: date-time */
            Since: string;
            Sort: string;
            Username: string;
        };
        SessionProjectRequest: {
            /** Format: int64 */
            project_id: number;
        };
        SessionProjectResponse: {
            pinned: boolean;
            /** Format: int64 */
            project_id: number;
            /** Format: int64 */
            session_id: number;
        };
        SessionPublicationResponse: {
            public_id?: string;
            published: boolean;
        };
        SessionRemainder: {
            /** Format: int64 */
            CacheRead: number;
            /** Format: int64 */
            CacheWrite: number;
            CostUSD: number;
            /** Format: int64 */
            Input: number;
            /** Format: int64 */
            Output: number;
            /** Format: int64 */
            Sessions: number;
        };
        SessionResponse: {
            can_delete: boolean;
            owner: boolean;
            snapshot: components["schemas"]["SessionSnapshot"];
        };
        SessionRow: {
            Agent: string;
            EndedAt: string | null;
            GitBranch: string;
            Grade: string | null;
            /** Format: int64 */
            ID: number;
            LastActiveAt: string | null;
            Machine: string;
            /** Format: int64 */
            MessageCount: number;
            /** Format: int64 */
            ModelFallbackCount: number;
            Outcome: string;
            /** Format: int64 */
            ProjectID: number;
            ProjectKey: string;
            ProjectKind: string;
            ProjectName: string;
            PublicID: string | null;
            Search: components["schemas"]["SearchSnippet"];
            StartedAt: string | null;
            Title: string;
            /** Format: int64 */
            TotalCacheRead: number;
            /** Format: int64 */
            TotalCacheWrite: number;
            TotalCostUSD: number;
            /** Format: int64 */
            TotalInput: number;
            /** Format: int64 */
            TotalOutput: number;
            Tree: components["schemas"]["TreeRollup"];
            /** Format: int64 */
            UserMessageCount: number;
            Username: string;
            Visibility: string;
        };
        SessionShape: {
            /** Format: int64 */
            DupIDs: number;
            Outline: components["schemas"]["Message"][];
            Tools: components["schemas"]["ToolCallView"][];
        };
        SessionSignals: {
            AssistantTurns: number | null;
            ContextResetCount: number | null;
            /** Format: int64 */
            DuplicatePromptCount: number;
            /** Format: int64 */
            EditChurn: number;
            Grade: string | null;
            /** Format: int64 */
            LongestFailureStreak: number;
            /** Format: int64 */
            NoCodeContextCount: number;
            Outcome: string;
            OutcomeConfidence: string;
            PeakContextTokens: number | null;
            /** Format: int64 */
            PromptCount: number;
            Score: number | null;
            /** Format: int64 */
            SessionID: number;
            /** Format: int64 */
            ShortPromptCount: number;
            ThinkingPeakTokens: number | null;
            ThinkingTailTokens: number | null;
            ThinkingTurns: number | null;
            /** Format: int64 */
            ToolCalls: number;
            /** Format: int64 */
            ToolFailures: number;
            /** Format: int64 */
            ToolRetries: number;
            UnstructuredStart: boolean;
        };
        SessionSnapshot: {
            Audit: components["schemas"]["SessionAudit"];
            Page: components["schemas"]["TranscriptPage"];
            Shape: components["schemas"]["SessionShape"] | null;
        };
        SessionSummary: {
            Agent: string;
            EndedAt: string | null;
            GitBranch: string;
            /** Format: int64 */
            ID: number;
            LastActiveAt: string | null;
            Machine: string;
            /** Format: int64 */
            MessageCount: number;
            /** Format: int64 */
            ModelFallbackCount: number;
            PublicID: string | null;
            StartedAt: string | null;
            Title: string;
            /** Format: int64 */
            TotalCacheRead: number;
            /** Format: int64 */
            TotalCacheWrite: number;
            TotalCostUSD: number;
            /** Format: int64 */
            TotalInput: number;
            /** Format: int64 */
            TotalOutput: number;
            /** Format: int64 */
            UserMessageCount: number;
            Username: string;
            Visibility: string;
        };
        SessionsResponse: {
            facets: components["schemas"]["GlobalFacetValues"];
            filter: components["schemas"]["SessionFilter"];
            has_more: boolean;
            sessions: components["schemas"]["SessionRow"][];
        };
        SignalTrends: {
            AbandonedCount: number[];
            AbandonedRate: number[];
            ArchetypeShare: {
                [key: string]: number;
            }[];
            CompletedCount: number[];
            CompletedRate: number[];
            ContextHistogram: components["schemas"]["ContextBucket"][];
            ContextMarkers: components["schemas"]["ContextMarker"][];
            ContextResets: number[];
            GPA: number[];
            GradeShare: {
                [key: string]: number;
            }[];
            HygieneNoCode: number[];
            HygieneRepeated: number[];
            HygieneTerse: number[];
            HygieneUnstructured: number[];
            OutcomeTotal: number[];
        };
        Status: {
            /** Format: int64 */
            done: number;
            /** Format: int64 */
            failed: number;
            in_progress: boolean;
            /** Format: date-time */
            started_at?: string;
            /** Format: int64 */
            total: number;
        };
        StatusResponse: {
            status: string;
        };
        SubagentRow: {
            Agent: string;
            EndedAt: string | null;
            GitBranch: string;
            Grade: string | null;
            /** Format: int64 */
            ID: number;
            LastActiveAt: string | null;
            Machine: string;
            /** Format: int64 */
            MessageCount: number;
            /** Format: int64 */
            ModelFallbackCount: number;
            Outcome: string;
            PublicID: string | null;
            StartedAt: string | null;
            SubagentName: string;
            Title: string;
            /** Format: int64 */
            TotalCacheRead: number;
            /** Format: int64 */
            TotalCacheWrite: number;
            TotalCostUSD: number;
            /** Format: int64 */
            TotalInput: number;
            /** Format: int64 */
            TotalOutput: number;
            /** Format: int64 */
            UserMessageCount: number;
            Username: string;
            Visibility: string;
        };
        SubagentStats: {
            CostShare: number[];
            CostThroughSubagentsPct: number;
            /** Format: int64 */
            DeepestTree: number;
            DelegateShare: number[];
            FanoutOrder: string[];
            FanoutRows: {
                [key: string]: number;
            }[];
            SessionsThatDelegatePct: number;
            /** Format: int64 */
            SubagentSessionsInWindow: number;
        };
        TokenListItem: {
            /** Format: date-time */
            created_at: string;
            /** Format: int64 */
            id: number;
            last_used_at: string | null;
            name: string;
            revoked_at: string | null;
            scope: string;
        };
        TokensResponse: {
            tokens: components["schemas"]["TokenListItem"][];
        };
        ToolCallView: {
            AttributionAgent: string;
            AttributionPlugin: string;
            AttributionSkill: string;
            /** Format: int64 */
            CallIndex: number;
            Category: string;
            Detail: string;
            FilePath: string;
            FileRelPath: string;
            /** Format: int64 */
            InputBytes: number;
            InputMediaType: string;
            InputSHA: string;
            /** Format: int64 */
            MessageOrdinal: number;
            /** Format: int64 */
            ResultBytes: number;
            ResultMediaType: string;
            ResultSHA: string;
            ResultStatus: string;
            /** Format: int64 */
            StructBytes: number;
            StructMediaType: string;
            StructSHA256: string;
            ToolName: string;
        };
        ToolFailSeries: {
            Name: string;
            Rate: number[];
        };
        ToolPoint: {
            /** Format: int64 */
            Calls: number;
            Category: string;
            /** Format: int64 */
            Failures: number;
            Name: string;
            /** Format: int64 */
            Sessions: number;
        };
        ToolStat: {
            /** Format: int64 */
            Calls: number;
            /** Format: int64 */
            Failures: number;
            Name: string;
        };
        ToolStats: {
            /** Format: int64 */
            Clipped: number;
            Tools: components["schemas"]["ToolStat"][];
            /** Format: int64 */
            TotalCalls: number;
            /** Format: int64 */
            TotalFailures: number;
            /** Format: int64 */
            Turns: number;
        };
        ToolTrends: {
            FailFleet: number[];
            FailWorst: components["schemas"]["ToolFailSeries"][];
            Mix: {
                [key: string]: number;
            }[];
            MixOrder: string[];
            Reliability: components["schemas"]["ToolPoint"][];
        };
        TranscriptPage: {
            Attachments: components["schemas"]["AttachmentView"][];
            /** Format: int64 */
            EarlierCount: number;
            Events: components["schemas"]["SessionEvent"][];
            Fallbacks: components["schemas"]["ModelFallback"][];
            HasEarlier: boolean;
            More: boolean;
            Msgs: components["schemas"]["Message"][];
            Seed: components["schemas"]["Message"][];
            Tools: components["schemas"]["ToolCallView"][];
        };
        TranscriptResponse: {
            page: components["schemas"]["TranscriptPage"];
        };
        TreeRollup: {
            CostUSD: number;
            /** Format: int64 */
            SubagentCount: number;
        };
        Trends: {
            BucketStarts: string[];
            Churn: components["schemas"]["ChurnTrend"];
            Economics: components["schemas"]["Economics"];
            FleetMix: components["schemas"]["FleetMix"];
            Gallery: components["schemas"]["Gallery"];
            Labels: string[];
            ModelCost: components["schemas"]["ModelCost"][];
            Rhythm: components["schemas"]["RhythmGrid"];
            Signals: components["schemas"]["SignalTrends"];
            Subagents: components["schemas"]["SubagentStats"];
            Tools: components["schemas"]["ToolTrends"];
            Unit: string;
            Velocity: components["schemas"]["VelocityTrends"];
        };
        TurnUsage: {
            /** Format: int64 */
            CacheRead: number;
            /** Format: int64 */
            CacheWrite: number;
            /** Format: int64 */
            ContextTokens: number;
            CostUSD: number;
            /** Format: int64 */
            Input: number;
            /** Format: int64 */
            Output: number;
            /** Format: int64 */
            Reasoning: number;
        };
        VelocityStats: {
            ActiveSeconds: number;
            /** Format: int64 */
            FirstResponseP50: number;
            MsgsPerActiveMin: number;
            /** Format: int64 */
            ResponseP50: number;
            /** Format: int64 */
            ResponseP90: number;
            /** Format: int64 */
            Sessions: number;
            ToolsPerActiveMin: number;
            /** Format: int64 */
            Turns: number;
        };
        VelocityTrends: {
            ActiveHours: number[];
            MsgsPerMin: number[];
            ResponseP50: number[];
            ResponseP90: number[];
            ResponseP99: number[];
            ToolsPerMin: number[];
            WallHours: number[];
        };
        Viewer: {
            authenticated: boolean;
            csrf_token?: string;
            is_admin: boolean;
            overview_public: boolean;
            /** Format: int64 */
            user_id?: number;
            username?: string;
            version: string;
        };
    };
    responses: {
        /** The request is invalid. */
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /** The requested identity already exists. */
        Conflict: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /** The credential does not have the required scope. */
        Forbidden: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /** The requested resource does not exist or is not visible. */
        NotFound: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
        /** The parsed projection is rebuilding; retry after the requested delay. */
        ProjectionRebuilding: {
            headers: {
                /** Seconds before retrying. */
                "Retry-After": number;
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ProjectionRebuild"];
            };
        };
        /** Authentication is required. */
        Unauthorized: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["Error"];
            };
        };
    };
}
