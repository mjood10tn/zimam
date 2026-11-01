<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('agent_actions', function (Blueprint $table) {
            $table->id();
            $table->string('command');
            // The panel user who asked; null for the queue and the scheduler.
            // No foreign key: the log outlives the account it names.
            $table->unsignedBigInteger('actor_id')->nullable()->index();
            $table->json('params')->nullable(); // redacted
            $table->string('outcome'); // accepted, ok or error:<code>
            $table->unsignedInteger('duration_ms');
            $table->timestamp('created_at')->nullable()->index();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('agent_actions');
    }
};
