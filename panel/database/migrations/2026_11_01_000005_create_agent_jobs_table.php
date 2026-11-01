<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/** The panel's record of each agent job it submitted (§7.6, D16). */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('agent_jobs', function (Blueprint $table) {
            $table->id();
            // The agent's ULID; null while the job waits after `busy` (D8).
            $table->string('agent_job_id', 26)->nullable()->unique();
            $table->string('command');
            $table->string('subject')->nullable();
            // waiting, queued, running, succeeded, failed or cancelled.
            $table->string('status')->index();
            $table->string('error_code')->nullable();
            $table->text('error_message')->nullable();
            // As in agent_actions: no foreign key, the history outlives the account.
            $table->unsignedBigInteger('created_by')->nullable()->index();
            // The command's RuntimeMaxSec, for TrackAgentJob::retryUntil().
            $table->unsignedInteger('runtime_max_seconds')->nullable();
            $table->timestamp('queued_at')->nullable();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('finished_at')->nullable();
            $table->json('params'); // redacted
            $table->json('result')->nullable();
            $table->nullableMorphs('related');
            $table->timestamps();
            $table->index('created_at');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('agent_jobs');
    }
};
